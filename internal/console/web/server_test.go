package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/go-logr/logr/funcr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/lsdopen/kiro-crew-operator/api/v1alpha1"
	"github.com/lsdopen/kiro-crew-operator/internal/console/auth"
	"github.com/lsdopen/kiro-crew-operator/internal/console/authz"
	"github.com/lsdopen/kiro-crew-operator/internal/console/oidctest"
)

const (
	ns         = "kiro-crews"
	testDomain = "example.com"
	csrfHeader = "X-CSRF-Token"
	htmxOn     = "true"
)

type env struct {
	t       *testing.T
	h       http.Handler
	iss     *oidctest.Issuer
	k8s     client.Client
	sealer  *auth.Sealer
	domains []string
	// sessions caches one session per email, for tests that need a stable sid.
	sessions map[string]*auth.Session
	// logs is everything the server logged; patches the bodies of crew patches.
	logs    *strings.Builder
	patches [][]byte
	srv     *Server
}

func newEnv(t *testing.T, domains []string, objs ...client.Object) *env {
	t.Helper()
	return newEnvBroker(t, domains, nil, objs...)
}

func newEnvBroker(t *testing.T, domains []string, br Opener, objs ...client.Object) *env {
	t.Helper()
	s := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, logs: &strings.Builder{}}
	k8s := fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).WithInterceptorFuncs(interceptor.Funcs{
		Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, p client.Patch, opts ...client.PatchOption) error {
			if _, ok := obj.(*v1alpha1.KiroCrew); ok {
				body, err := p.Data(obj)
				if err != nil {
					return err
				}
				e.patches = append(e.patches, body)
			}
			return c.Patch(ctx, obj, p, opts...)
		},
	}).Build()
	iss, err := oidctest.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(iss.Close)
	provider, err := auth.NewOIDC(context.Background(), auth.OIDCConfig{
		IssuerURL: iss.URL, RedirectURL: "https://console.example.com/auth/callback",
		ClientID: oidctest.ClientID, ClientSecret: "secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	sealer, err := auth.NewSealer([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	log := funcr.New(func(prefix, args string) { e.logs.WriteString(prefix + " " + args + "\n") }, funcr.Options{Verbosity: 10})
	srv := &Server{
		Provider: provider, Sealer: sealer, AllowedDomains: domains, Log: log, Broker: br,
		Store: &authz.Store{Client: k8s, Live: k8s, CrewNamespace: ns},
	}
	e.srv, e.h, e.iss, e.k8s, e.sealer, e.domains = srv, srv.Handler(), iss, k8s, sealer, domains
	return e
}

// do sends a request carrying the given cookies and returns the recorded response.
func (e *env) do(method, target string, cookies []*http.Cookie, hdr map[string]string, body string) *httptest.ResponseRecorder {
	e.t.Helper()
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for _, c := range cookies {
		r.AddCookie(c)
	}
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, r)
	return w
}

func cookie(w *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range w.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// signIn runs the whole flow against the in-memory issuer and returns the callback
// response. overrides replace ID token claims; a nil value deletes the claim.
func (e *env) signIn(overrides map[string]any) *httptest.ResponseRecorder {
	e.t.Helper()
	start := e.do(http.MethodGet, "/auth/login", nil, nil, "")
	if start.Code != http.StatusFound {
		e.t.Fatalf("login status = %d", start.Code)
	}
	loc := start.Header().Get("Location")
	u, _ := url.Parse(loc)
	code, err := e.iss.Authorize(loc, overrides)
	if err != nil {
		e.t.Fatal(err)
	}
	q := url.Values{"code": {code}, "state": {u.Query().Get("state")}}
	return e.do(http.MethodGet, "/auth/callback?"+q.Encode(), []*http.Cookie{cookie(start, auth.OAuthCookie)}, nil, "")
}

func (e *env) accounts() []v1alpha1.KiroCrewAccount {
	e.t.Helper()
	var l v1alpha1.KiroCrewAccountList
	if err := e.k8s.List(context.Background(), &l); err != nil {
		e.t.Fatal(err)
	}
	return l.Items
}

// sessionFor mints a session cookie for email without going through login.
func (e *env) sessionFor(email string) (*http.Cookie, *auth.Session) {
	e.t.Helper()
	sess, err := e.sealer.NewSession(email)
	if err != nil {
		e.t.Fatal(err)
	}
	c, err := e.sealer.SessionCookie(sess)
	if err != nil {
		e.t.Fatal(err)
	}
	return c, sess
}

func acct(email string, role v1alpha1.Role, manager string) *v1alpha1.KiroCrewAccount {
	return &v1alpha1.KiroCrewAccount{
		ObjectMeta: metav1.ObjectMeta{Name: authz.AccountName(email)},
		Spec:       v1alpha1.KiroCrewAccountSpec{Email: email, Role: role, Manager: manager},
	}
}

func TestLoginCreatesOneUserAccountAndASession(t *testing.T) {
	e := newEnv(t, []string{testDomain})
	for range 2 {
		w := e.signIn(nil)
		if w.Code != http.StatusSeeOther || w.Header().Get("Location") != crewsPath {
			t.Fatalf("callback = %d %s", w.Code, w.Header().Get("Location"))
		}
		s := cookie(w, auth.SessionCookie)
		if s == nil || !s.Secure || !s.HttpOnly || s.SameSite != http.SameSiteLaxMode || s.Expires.IsZero() {
			t.Fatalf("session cookie = %+v", s)
		}
		if o := cookie(w, auth.OAuthCookie); o == nil || o.MaxAge >= 0 {
			t.Fatalf("login state cookie not cleared: %+v", o)
		}
	}
	got := e.accounts()
	if len(got) != 1 || got[0].Spec.Email != "alice@example.com" || got[0].Spec.Role != v1alpha1.RoleUser ||
		got[0].Spec.Manager != "" {
		t.Fatalf("accounts = %+v", got)
	}
}

func TestLoginRefusals(t *testing.T) {
	tests := []struct {
		name      string
		domains   []string
		overrides map[string]any
	}{
		{"wrong hd", []string{testDomain}, map[string]any{"hd": "evil.org", "email": "a@evil.org"}},
		{"missing hd", []string{testDomain}, map[string]any{"hd": nil}},
		{"email_verified false", []string{testDomain}, map[string]any{"email_verified": false}},
		{"email_verified missing", []string{testDomain}, map[string]any{"email_verified": nil}},
		{"email/hd mismatch", []string{testDomain}, map[string]any{"email": "alice@evil.org"}},
		{"empty allowed list", nil, nil},
		{"wrong nonce", []string{testDomain}, map[string]any{"nonce": "not-the-one"}},
		{"wrong audience", []string{testDomain}, map[string]any{"aud": "other"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, tt.domains)
			w := e.signIn(tt.overrides)
			if w.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403", w.Code)
			}
			if cookie(w, auth.SessionCookie) != nil {
				t.Fatal("refused login set a session")
			}
			if n := len(e.accounts()); n != 0 {
				t.Fatalf("refused login created %d accounts", n)
			}
		})
	}
}

func TestLoginRefusesForgedSignature(t *testing.T) {
	e := newEnv(t, []string{testDomain})
	e.iss.Forge = true
	if w := e.signIn(nil); w.Code != http.StatusForbidden || len(e.accounts()) != 0 {
		t.Fatalf("status = %d, accounts = %d", w.Code, len(e.accounts()))
	}
}

func TestCallbackRejectsBadState(t *testing.T) {
	e := newEnv(t, []string{testDomain})
	start := e.do(http.MethodGet, "/auth/login", nil, nil, "")
	code, _ := e.iss.Authorize(start.Header().Get("Location"), nil)
	oc := []*http.Cookie{cookie(start, auth.OAuthCookie)}

	if w := e.do(http.MethodGet, "/auth/callback?code="+code+"&state=forged", oc, nil, ""); w.Code != http.StatusBadRequest {
		t.Errorf("forged state = %d", w.Code)
	}
	if w := e.do(http.MethodGet, "/auth/callback?code="+code+"&state=x", nil, nil, ""); w.Code != http.StatusBadRequest {
		t.Errorf("no login cookie = %d", w.Code)
	}
	if n := len(e.accounts()); n != 0 {
		t.Errorf("created %d accounts", n)
	}
}

func TestLoginAuthURL(t *testing.T) {
	e := newEnv(t, []string{testDomain})
	w := e.do(http.MethodGet, "/auth/login?return=//evil.org", nil, nil, "")
	u, _ := url.Parse(w.Header().Get("Location"))
	q := u.Query()
	if q.Get("state") == "" || q.Get("nonce") == "" || q.Get("code_challenge_method") != "S256" ||
		q.Get("hd") != testDomain {
		t.Fatalf("auth URL = %s", u)
	}
	st, err := e.sealer.OpenOAuth(func() *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.AddCookie(cookie(w, auth.OAuthCookie))
		return r
	}())
	if err != nil || st.Return != crewsPath {
		t.Fatalf("open redirect not neutralised: %+v %v", st, err)
	}
	// Two domains: no hd hint.
	e2 := newEnv(t, []string{testDomain, "example.org"})
	u2, _ := url.Parse(e2.do(http.MethodGet, "/auth/login", nil, nil, "").Header().Get("Location"))
	if u2.Query().Get("hd") != "" {
		t.Error("hd hint sent with several domains")
	}
}

func TestLoginReturnIsSameSiteOnly(t *testing.T) {
	e := newEnv(t, []string{testDomain})
	tests := []struct{ name, query, want string }{
		{"plain path", pathRequests, pathRequests},
		{"path with query", "/crews%3Fa%3D1", "/crews?a=1"},
		{"protocol-relative", "//evil.org", crewsPath},
		{"encoded tab", "/%09/evil.com", crewsPath},
		{"encoded backslash", "/%5Cevil.com", crewsPath},
		{"encoded CRLF", "/%0d%0a", crewsPath},
		{"absolute URL", "https://evil.com", crewsPath},
		{"triple slash", "///evil.com", crewsPath},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := e.do(http.MethodGet, "/auth/login?return="+tt.query, nil, nil, "")
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.AddCookie(cookie(w, auth.OAuthCookie))
			st, err := e.sealer.OpenOAuth(r)
			if err != nil || st.Return != tt.want {
				t.Fatalf("return = %q (%v), want %q", st.Return, err, tt.want)
			}
		})
	}
}

func TestSafeReturn(t *testing.T) {
	for _, in := range []string{
		"//evil.org", "/\t/evil.com", "/\\evil.com", "/%09/evil.com", "/%0d%0a", "/\r\n",
		"https://evil.com", "///evil.com", "evil.com", "", "/\x7f", "/a\x00b",
	} {
		if got := safeReturn(in); got != crewsPath {
			t.Errorf("safeReturn(%q) = %q, want /crews", in, got)
		}
	}
	for _, in := range []string{"/crews", "/requests?x=1", "/accounts"} {
		if got := safeReturn(in); got != in {
			t.Errorf("safeReturn(%q) = %q, want unchanged", in, got)
		}
	}
}

func TestUnauthenticated(t *testing.T) {
	e := newEnv(t, []string{testDomain})
	w := e.do(http.MethodGet, "/crews", nil, nil, "")
	if w.Code != http.StatusFound || !strings.HasPrefix(w.Header().Get("Location"), "/auth/login") {
		t.Errorf("page = %d %s", w.Code, w.Header().Get("Location"))
	}
	w = e.do(http.MethodGet, "/crews", nil, map[string]string{"HX-Request": htmxOn}, "")
	if w.Code != http.StatusUnauthorized || w.Header().Get("HX-Redirect") != "/auth/login" {
		t.Errorf("htmx = %d %s", w.Code, w.Header().Get("HX-Redirect"))
	}
}

func TestSessionWithoutAccountIsRejected(t *testing.T) {
	e := newEnv(t, []string{testDomain})
	c, _ := e.sessionFor("ghost@example.com")
	w := e.do(http.MethodGet, "/crews", []*http.Cookie{c}, nil, "")
	if w.Code != http.StatusFound || len(e.accounts()) != 0 {
		t.Fatalf("status = %d, accounts = %d", w.Code, len(e.accounts()))
	}
}

func TestLogoutRequiresCSRF(t *testing.T) {
	e := newEnv(t, []string{testDomain}, acct("alice@example.com", v1alpha1.RoleUser, ""))
	c, sess := e.sessionFor("alice@example.com")
	good := e.sealer.CSRFToken(sess.SID)
	ck := []*http.Cookie{c}

	if w := e.do(http.MethodPost, "/auth/logout", ck, nil, ""); w.Code != http.StatusForbidden {
		t.Errorf("no token = %d", w.Code)
	}
	if w := e.do(http.MethodPost, "/auth/logout", ck, map[string]string{csrfHeader: badToken}, ""); w.Code != http.StatusForbidden {
		t.Errorf("bad token = %d", w.Code)
	}
	other := e.sealer.CSRFToken("another-session")
	if w := e.do(http.MethodPost, "/auth/logout", ck, map[string]string{csrfHeader: other}, ""); w.Code != http.StatusForbidden {
		t.Errorf("another session's token = %d", w.Code)
	}
	cross := map[string]string{csrfHeader: good, "Sec-Fetch-Site": "cross-site"}
	if w := e.do(http.MethodPost, "/auth/logout", ck, cross, ""); w.Code != http.StatusForbidden {
		t.Errorf("cross-site with valid token = %d", w.Code)
	}
	if w := e.do(http.MethodPost, "/auth/logout", ck, map[string]string{csrfHeader: good}, ""); w.Code != http.StatusSeeOther {
		t.Errorf("header token = %d", w.Code)
	} else if s := cookie(w, auth.SessionCookie); s == nil || s.MaxAge >= 0 {
		t.Errorf("session not cleared: %+v", s)
	}
	if w := e.do(http.MethodPost, "/auth/logout", ck, nil, "csrf_token="+good); w.Code != http.StatusSeeOther {
		t.Errorf("form token = %d", w.Code)
	}
}

func crewWith(name, owner, dashboard, loginURL string) *v1alpha1.KiroCrew {
	return &v1alpha1.KiroCrew{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: v1alpha1.KiroCrewSpec{Owner: owner, Schedule: &v1alpha1.ScheduleSpec{
			Up: cronUp, Down: cronDown, Timezone: "UTC"}},
		Status: v1alpha1.KiroCrewStatus{DashboardURL: dashboard, TailnetLoginURL: loginURL},
	}
}

func TestViewsAreRoleScoped(t *testing.T) {
	e := newEnv(t, []string{testDomain},
		acct("alice@example.com", v1alpha1.RoleUser, "mgr@example.com"),
		acct("bob@example.com", v1alpha1.RoleUser, "mgr@example.com"),
		acct("mgr@example.com", v1alpha1.RoleManager, ""),
		acct("root@example.com", v1alpha1.RoleAdmin, ""),
		crewWith("alice", "alice@example.com", "https://alice.example.ts.net", "https://login.example.com/a"),
		crewWith("bob", "bob@example.com", "https://bob.example.ts.net", "https://login.example.com/b"),
		&v1alpha1.KiroCrewRequest{
			ObjectMeta: metav1.ObjectMeta{Name: "req-bob", Namespace: ns},
			Spec:       v1alpha1.KiroCrewRequestSpec{Requester: "bob@example.com", Reason: "bob-reason"},
		})

	body := func(email, path string) (int, string) {
		c, _ := e.sessionFor(email)
		w := e.do(http.MethodGet, path, []*http.Cookie{c}, nil, "")
		return w.Code, w.Body.String()
	}

	_, alice := body("alice@example.com", "/crews")
	if !strings.Contains(alice, "https://alice.example.ts.net") || !strings.Contains(alice, "0 8 * * 1-5") {
		t.Error("owner does not see her dashboard link and schedule")
	}
	if strings.Contains(alice, "bob") || strings.Contains(alice, "bob.example.ts.net") {
		t.Error("user sees another user's crew")
	}
	if _, reqs := body("alice@example.com", "/requests"); strings.Contains(reqs, "req-bob") {
		t.Error("user sees another user's request")
	}
	if code, _ := body("alice@example.com", "/accounts"); code != http.StatusForbidden {
		t.Errorf("user /accounts = %d", code)
	}

	_, bob := body("bob@example.com", "/crews")
	if !strings.Contains(bob, "https://bob.example.ts.net") || strings.Contains(bob, "alice.example.ts.net") {
		t.Error("bob's view is wrong")
	}

	_, mgr := body("mgr@example.com", "/crews")
	if !strings.Contains(mgr, ">alice<") || !strings.Contains(mgr, ">bob<") {
		t.Error("manager does not see reports' crews")
	}
	if strings.Contains(mgr, "alice.example.ts.net") || strings.Contains(mgr, "login.example.com") {
		t.Error("manager sees links into reports' crews")
	}
	if _, reqs := body("mgr@example.com", "/requests"); !strings.Contains(reqs, "req-bob") {
		t.Error("manager does not see reports' requests")
	}
	if code, _ := body("mgr@example.com", "/accounts"); code != http.StatusForbidden {
		t.Errorf("manager /accounts = %d", code)
	}

	_, root := body("root@example.com", "/crews")
	if !strings.Contains(root, ">alice<") || !strings.Contains(root, ">bob<") || strings.Contains(root, "bob.example.ts.net") {
		t.Error("admin view is wrong")
	}
	if code, accts := body("root@example.com", "/accounts"); code != http.StatusOK || !strings.Contains(accts, "mgr@example.com") {
		t.Errorf("admin /accounts = %d", code)
	}
}

func TestNodeAuthLinkShownToOwnerUntilJoined(t *testing.T) {
	e := newEnv(t, []string{testDomain},
		acct("alice@example.com", v1alpha1.RoleUser, ""),
		crewWith("alice", "alice@example.com", "", "https://login.example.com/a"))
	c, _ := e.sessionFor("alice@example.com")
	w := e.do(http.MethodGet, "/crews", []*http.Cookie{c}, nil, "")
	if !strings.Contains(w.Body.String(), "Authenticate your crew node") {
		t.Error("owner does not get the node auth link")
	}
}

func TestHostileStatusURLsAreNotLinked(t *testing.T) {
	e := newEnv(t, []string{testDomain},
		acct("alice@example.com", v1alpha1.RoleUser, ""),
		crewWith("alice", "alice@example.com", "javascript:alert(1)", "http://plain.example.com"))
	c, _ := e.sessionFor("alice@example.com")
	body := e.do(http.MethodGet, "/crews", []*http.Cookie{c}, nil, "").Body.String()
	if strings.Contains(body, "javascript:") || strings.Contains(body, "plain.example.com") {
		t.Error("non-https status URL rendered as a link")
	}
}

func TestHTMXGetsFragmentAndHeaders(t *testing.T) {
	e := newEnv(t, []string{testDomain}, acct("alice@example.com", v1alpha1.RoleUser, ""))
	c, sess := e.sessionFor("alice@example.com")
	full := e.do(http.MethodGet, "/crews", []*http.Cookie{c}, nil, "")
	frag := e.do(http.MethodGet, "/crews", []*http.Cookie{c}, map[string]string{"HX-Request": htmxOn}, "")
	if !strings.Contains(full.Body.String(), "<html") || strings.Contains(frag.Body.String(), "<html") {
		t.Error("full page / fragment split is wrong")
	}
	if !strings.Contains(full.Body.String(), e.sealer.CSRFToken(sess.SID)) {
		t.Error("page does not carry the CSRF token for htmx")
	}
	h := full.Header()
	if !strings.Contains(h.Get("Content-Security-Policy"), "frame-ancestors 'none'") || h.Get("Cache-Control") != "no-store" ||
		h.Get("Strict-Transport-Security") == "" || h.Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("headers = %v", h)
	}
	if w := e.do(http.MethodGet, "/static/htmx.min.js", nil, nil, ""); w.Code != http.StatusOK || w.Body.Len() < 1000 {
		t.Errorf("htmx asset = %d, %d bytes", w.Code, w.Body.Len())
	}
}
