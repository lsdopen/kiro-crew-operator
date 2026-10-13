package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/lsdopen/kiro-crew-operator/api/v1alpha1"
	"github.com/lsdopen/kiro-crew-operator/internal/console/auth"
)

const (
	alice = "alice@example.com"
	bob   = "bob@example.com"
	mgr   = "mgr@example.com"
	root  = "root@example.com"

	cronUp, cronDown = "0 8 * * 1-5", "0 18 * * 1-5"
	approved         = "approved"
	sizeLight        = "light"
	nope             = "nope"
	rAlice           = "r-alice"
	pathRequests     = "/requests"
	badToken         = "bad"
	keyDown          = "down"
	keyZone          = "timezone"
	keySize          = "size"
	keyVerdict       = "verdict"
)

func people(extra ...client.Object) []client.Object {
	return append([]client.Object{
		acct(alice, v1alpha1.RoleUser, mgr),
		acct(bob, v1alpha1.RoleUser, "other@example.com"),
		acct(mgr, v1alpha1.RoleManager, ""),
		acct(root, v1alpha1.RoleAdmin, ""),
		acct("orphan@example.com", v1alpha1.RoleUser, ""),
	}, extra...)
}

func pendingReq(name, requester string) *v1alpha1.KiroCrewRequest {
	return &v1alpha1.KiroCrewRequest{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec:       v1alpha1.KiroCrewRequestSpec{Requester: requester, Size: v1alpha1.SizeLight},
	}
}

// post sends a form as email with a valid CSRF header, plus extra headers.
func (e *env) post(email, path string, form url.Values, hdr ...string) *httptest.ResponseRecorder {
	e.t.Helper()
	c, sess := e.stableSession(email)
	h := map[string]string{csrfHeader: e.sealer.CSRFToken(sess.SID)}
	for i := 0; i+1 < len(hdr); i += 2 {
		h[hdr[i]] = hdr[i+1]
	}
	return e.do(http.MethodPost, path, []*http.Cookie{c}, h, form.Encode())
}

// stableSession returns the same session for an email on every call.
func (e *env) stableSession(email string) (*http.Cookie, *auth.Session) {
	e.t.Helper()
	if e.sessions == nil {
		e.sessions = map[string]*auth.Session{}
	}
	sess := e.sessions[email]
	if sess == nil {
		_, sess = e.sessionFor(email)
		e.sessions[email] = sess
	}
	c, err := e.sealer.SessionCookie(sess)
	if err != nil {
		e.t.Fatal(err)
	}
	return c, sess
}

func (e *env) requests() []v1alpha1.KiroCrewRequest {
	e.t.Helper()
	var l v1alpha1.KiroCrewRequestList
	if err := e.k8s.List(context.Background(), &l); err != nil {
		e.t.Fatal(err)
	}
	return l.Items
}

func (e *env) request(name string) *v1alpha1.KiroCrewRequest {
	e.t.Helper()
	q := &v1alpha1.KiroCrewRequest{}
	if err := e.k8s.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: name}, q); err != nil {
		e.t.Fatal(err)
	}
	return q
}

func TestUserCreatesRequestAsSelf(t *testing.T) {
	e := newEnv(t, []string{testDomain}, people()...)
	w := e.post(alice, pathRequests, url.Values{
		keySize: {"power"}, "up": {cronUp}, keyDown: {cronDown}, keyZone: {"Africa/Johannesburg"},
		"reason": {"  need it  "},
		// Identity fields must be ignored.
		"requester": {bob}, "by": {root}, "decision": {approved}, keyVerdict: {approved},
	})
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != pathRequests {
		t.Fatalf("status = %d %s", w.Code, w.Header().Get("Location"))
	}
	got := e.requests()
	if len(got) != 1 {
		t.Fatalf("requests = %d", len(got))
	}
	q := got[0]
	if q.Spec.Requester != alice || q.Namespace != ns || q.Spec.Decision != nil || q.Spec.Size != v1alpha1.SizePower ||
		q.Spec.Reason != "need it" || q.Spec.Schedule == nil || q.Spec.Schedule.Timezone != "Africa/Johannesburg" {
		t.Fatalf("request = %+v", q.Spec)
	}
}

func TestRequestWithoutScheduleDefaultsToAlwaysOn(t *testing.T) {
	e := newEnv(t, []string{testDomain}, people()...)
	if w := e.post(alice, pathRequests, url.Values{keySize: {"mini"}}); w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d", w.Code)
	}
	if q := e.requests()[0]; q.Spec.Schedule != nil || q.Spec.Reason != "" {
		t.Fatalf("request = %+v", q.Spec)
	}
}

func TestHTMXCreateRendersRequestsFragment(t *testing.T) {
	e := newEnv(t, []string{testDomain}, people()...)
	w := e.post(alice, pathRequests, url.Values{keySize: {sizeLight}}, "HX-Request", "true")
	body := w.Body.String()
	if w.Code != http.StatusOK || strings.Contains(body, "<html") || !strings.Contains(body, "Request submitted.") ||
		!strings.Contains(body, "Cancel") {
		t.Fatalf("status = %d body = %s", w.Code, body)
	}
}

func TestInvalidRequestInputIsRefused(t *testing.T) {
	tests := []struct {
		name string
		form url.Values
	}{
		{"bad cron up", url.Values{keySize: {sizeLight}, "up": {nope}, keyDown: {cronDown}}},
		{"bad cron down", url.Values{keySize: {sizeLight}, "up": {cronUp}, keyDown: {"61 * * * *"}}},
		{"six fields", url.Values{keySize: {sizeLight}, "up": {"0 0 8 * * 1-5"}, keyDown: {cronDown}}},
		{"bad timezone", url.Values{keySize: {sizeLight}, "up": {cronUp}, keyDown: {cronDown}, keyZone: {"Mars/Base"}}},
		{"up without down", url.Values{keySize: {sizeLight}, "up": {cronUp}}},
		{"timezone alone", url.Values{keySize: {sizeLight}, keyZone: {"UTC"}}},
		{"unknown size", url.Values{keySize: {"huge"}}},
		{"no size", url.Values{}},
		{"long reason", url.Values{keySize: {sizeLight}, "reason": {strings.Repeat("x", 501)}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, []string{testDomain}, people()...)
			if w := e.post(alice, pathRequests, tt.form); w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", w.Code)
			}
			if n := len(e.requests()); n != 0 {
				t.Fatalf("%d requests written", n)
			}
		})
	}
}

func TestHTMXErrorIsSwappedIntoFlash(t *testing.T) {
	e := newEnv(t, []string{testDomain}, people()...)
	w := e.post(alice, pathRequests, url.Values{keySize: {sizeLight}, "up": {nope}, keyDown: {"x"}}, "HX-Request", "true")
	if w.Code != http.StatusBadRequest || w.Header().Get("HX-Retarget") != "#flash" ||
		!strings.Contains(w.Body.String(), `role="alert"`) {
		t.Fatalf("status = %d headers = %v body = %s", w.Code, w.Header(), w.Body.String())
	}
}

func TestSecondOpenRequestOrExistingCrewIsRefused(t *testing.T) {
	e := newEnv(t, []string{testDomain}, people(pendingReq("r1", alice), crewWith("bob", bob, "", ""))...)
	if w := e.post(alice, pathRequests, url.Values{keySize: {sizeLight}}); w.Code != http.StatusConflict {
		t.Errorf("open request: status = %d", w.Code)
	}
	if w := e.post(bob, pathRequests, url.Values{keySize: {sizeLight}}); w.Code != http.StatusConflict {
		t.Errorf("owns crew: status = %d", w.Code)
	}
	if n := len(e.requests()); n != 1 {
		t.Errorf("requests = %d", n)
	}
}

func TestManagerApprovesReportAsSelf(t *testing.T) {
	e := newEnv(t, []string{testDomain}, people(pendingReq("r1", alice))...)
	w := e.post(mgr, "/requests/r1/decision", url.Values{keyVerdict: {approved}, "by": {root}, "requester": {bob}})
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d", w.Code)
	}
	q := e.request("r1")
	if q.Spec.Decision == nil || q.Spec.Decision.By != mgr || q.Spec.Decision.Verdict != v1alpha1.VerdictApproved ||
		q.Spec.Requester != alice {
		t.Fatalf("spec = %+v decision = %+v", q.Spec, q.Spec.Decision)
	}
}

func TestManagerDenies(t *testing.T) {
	e := newEnv(t, []string{testDomain}, people(pendingReq("r1", alice))...)
	if w := e.post(mgr, "/requests/r1/decision", url.Values{keyVerdict: {"denied"}}, "HX-Request", "true"); w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if d := e.request("r1").Spec.Decision; d == nil || d.Verdict != v1alpha1.VerdictDenied || d.By != mgr {
		t.Fatalf("decision = %+v", d)
	}
}

func TestForbiddenDecisionsWriteNothing(t *testing.T) {
	tests := []struct {
		name, who, req string
	}{
		{"manager of someone else", mgr, "r-bob"},
		{"manager of nobody", mgr, "r-orphan"},
		{"user is not a manager", alice, "r-bob"},
		{"user approves own", alice, rAlice},
		{"user approves a report", bob, rAlice},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, []string{testDomain}, people(
				pendingReq(rAlice, alice), pendingReq("r-bob", bob), pendingReq("r-orphan", "orphan@example.com"))...)
			w := e.post(tt.who, "/requests/"+tt.req+"/decision", url.Values{keyVerdict: {approved}})
			if w.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403", w.Code)
			}
			for _, q := range e.requests() {
				if q.Spec.Decision != nil {
					t.Fatalf("%s was written: %+v", q.Name, q.Spec.Decision)
				}
			}
		})
	}
}

func TestUserNamedAsManagerStillCannotApprove(t *testing.T) {
	e := newEnv(t, []string{testDomain},
		acct(alice, v1alpha1.RoleUser, ""), acct(bob, v1alpha1.RoleUser, alice), pendingReq("r1", bob))
	if w := e.post(alice, "/requests/r1/decision", url.Values{keyVerdict: {approved}}); w.Code != http.StatusForbidden {
		t.Fatalf("status = %d", w.Code)
	}
	if e.request("r1").Spec.Decision != nil {
		t.Fatal("decision written")
	}
}

func TestAdminDecidesAnyRequest(t *testing.T) {
	e := newEnv(t, []string{testDomain}, people(
		pendingReq(rAlice, alice), pendingReq("r-orphan", "orphan@example.com"), pendingReq("r-ghost", "ghost@example.com"))...)
	for _, name := range []string{rAlice, "r-orphan", "r-ghost"} {
		if w := e.post(root, "/requests/"+name+"/decision", url.Values{keyVerdict: {approved}}); w.Code != http.StatusSeeOther {
			t.Fatalf("%s: status = %d", name, w.Code)
		}
		if d := e.request(name).Spec.Decision; d == nil || d.By != root {
			t.Fatalf("%s: decision = %+v", name, d)
		}
	}
}

func TestOnlyPendingRequestsCanBeDecided(t *testing.T) {
	decided := pendingReq("r-decided", alice)
	decided.Spec.Decision = &v1alpha1.Decision{Verdict: v1alpha1.VerdictDenied, By: mgr}
	done := pendingReq("r-done", alice)
	done.Status.Phase = v1alpha1.PhaseProvisioned
	invalid := pendingReq("r-invalid", alice)
	invalid.Status.Phase = v1alpha1.PhaseInvalid
	e := newEnv(t, []string{testDomain}, people(decided, done, invalid)...)

	for _, name := range []string{"r-decided", "r-done", "r-invalid"} {
		if w := e.post(root, "/requests/"+name+"/decision", url.Values{keyVerdict: {approved}}); w.Code != http.StatusConflict {
			t.Errorf("%s: status = %d, want 409", name, w.Code)
		}
	}
	if d := e.request("r-decided").Spec.Decision; d.By != mgr || d.Verdict != v1alpha1.VerdictDenied {
		t.Errorf("existing decision overwritten: %+v", d)
	}
	for _, name := range []string{"r-done", "r-invalid"} {
		if e.request(name).Spec.Decision != nil {
			t.Errorf("%s was written", name)
		}
	}
}

func TestDecisionInputIsValidated(t *testing.T) {
	e := newEnv(t, []string{testDomain}, people(pendingReq("r1", alice))...)
	for _, v := range []string{"", "maybe", "APPROVED"} {
		if w := e.post(root, "/requests/r1/decision", url.Values{keyVerdict: {v}}); w.Code != http.StatusBadRequest {
			t.Errorf("verdict %q: status = %d", v, w.Code)
		}
	}
	if w := e.post(root, "/requests/nope/decision", url.Values{keyVerdict: {approved}}); w.Code != http.StatusNotFound {
		t.Errorf("unknown request: status = %d", w.Code)
	}
	if e.request("r1").Spec.Decision != nil {
		t.Fatal("decision written")
	}
}

func TestWritesRequireCSRF(t *testing.T) {
	e := newEnv(t, []string{testDomain}, people(pendingReq("r1", alice))...)
	c, sess := e.sessionFor(root)
	ck := []*http.Cookie{c}
	good := e.sealer.CSRFToken(sess.SID)
	cases := map[string]map[string]string{
		"missing":         nil,
		badToken:          {csrfHeader: badToken},
		"other session":   {csrfHeader: e.sealer.CSRFToken("another")},
		"cross-site good": {csrfHeader: good, "Sec-Fetch-Site": "cross-site"},
	}
	for name, hdr := range cases {
		for path, body := range map[string]string{
			pathRequests:            "size=light",
			"/requests/r1/decision": "verdict=approved",
			"/requests/r1/cancel":   "",
		} {
			if w := e.do(http.MethodPost, path, ck, hdr, body); w.Code != http.StatusForbidden {
				t.Errorf("%s %s: status = %d, want 403", name, path, w.Code)
			}
		}
	}
	if q := e.request("r1"); q.Spec.Decision != nil || len(e.requests()) != 1 {
		t.Fatal("a refused post wrote something")
	}
	// A form field token is accepted, as for logout.
	if w := e.do(http.MethodPost, "/requests/r1/decision", ck, nil, "verdict=approved&csrf_token="+good); w.Code != http.StatusSeeOther {
		t.Errorf("form token: status = %d", w.Code)
	}
}

func TestUnauthenticatedWriteIsRefused(t *testing.T) {
	e := newEnv(t, []string{testDomain}, people(pendingReq("r1", alice))...)
	w := e.do(http.MethodPost, "/requests/r1/decision", nil, nil, "verdict=approved")
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/auth/login" {
		t.Fatalf("status = %d %s", w.Code, w.Header().Get("Location"))
	}
	if e.request("r1").Spec.Decision != nil {
		t.Fatal("decision written")
	}
}

func TestCancelOwnPendingRequest(t *testing.T) {
	e := newEnv(t, []string{testDomain}, people(pendingReq("r1", alice))...)
	if w := e.post(alice, "/requests/r1/cancel", nil); w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d", w.Code)
	}
	if n := len(e.requests()); n != 0 {
		t.Fatalf("requests = %d", n)
	}
}

func TestCancelRefusals(t *testing.T) {
	decided := pendingReq("r-decided", alice)
	decided.Spec.Decision = &v1alpha1.Decision{Verdict: v1alpha1.VerdictApproved, By: mgr}
	e := newEnv(t, []string{testDomain}, people(pendingReq("r1", alice), decided)...)

	for _, who := range []string{bob, mgr, root} {
		if w := e.post(who, "/requests/r1/cancel", nil); w.Code != http.StatusForbidden {
			t.Errorf("%s cancelling alice's: status = %d", who, w.Code)
		}
	}
	if w := e.post(alice, "/requests/r-decided/cancel", nil); w.Code != http.StatusConflict {
		t.Errorf("decided: status = %d", w.Code)
	}
	if n := len(e.requests()); n != 2 {
		t.Fatalf("requests = %d", n)
	}
}

func TestRequestButtonsFollowRole(t *testing.T) {
	e := newEnv(t, []string{testDomain}, people(pendingReq(rAlice, alice), pendingReq("r-bob", bob))...)
	view := func(email string) string {
		c, _ := e.sessionFor(email)
		return e.do(http.MethodGet, pathRequests, []*http.Cookie{c}, nil, "").Body.String()
	}
	if b := view(alice); strings.Contains(b, "Approve") || !strings.Contains(b, "Cancel") {
		t.Error("user should see Cancel and no Approve")
	}
	b := view(mgr)
	if strings.Count(b, "Approve") != 1 || strings.Contains(b, "/requests/r-bob/") || strings.Contains(b, "Cancel") {
		t.Errorf("manager sees buttons for the wrong requests: %s", b)
	}
	if b := view(root); strings.Count(b, "Approve") != 2 {
		t.Error("admin should be able to decide both")
	}
	if strings.Contains(view(bob), "No manager set") {
		t.Error("bob has a manager and should not see the warning")
	}
	if !strings.Contains(view("orphan@example.com"), "No manager set") {
		t.Error("a user without a manager should be warned")
	}
}

func TestWriteRateLimitIsPerSession(t *testing.T) {
	e := newEnv(t, []string{testDomain}, people(pendingReq("r1", alice))...)
	bad := url.Values{keyVerdict: {nope}}
	var last int
	for range writeBurst + 2 {
		last = e.post(root, "/requests/r1/decision", bad).Code
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("status after burst = %d, want 429", last)
	}
	// Another session is unaffected.
	if w := e.post(mgr, "/requests/r1/decision", url.Values{keyVerdict: {approved}}); w.Code != http.StatusSeeOther {
		t.Fatalf("other session: status = %d", w.Code)
	}
}

func TestLimiterRefillsAndSweeps(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	l := newLimiter(2, time.Second)
	l.now = func() time.Time { return now }
	l.allow("a")
	l.allow("a")
	if l.allow("a") {
		t.Fatal("burst of 2 not enforced")
	}
	if !l.allow("b") {
		t.Fatal("keys share a bucket")
	}
	now = now.Add(1500 * time.Millisecond)
	if !l.allow("a") || l.allow("a") {
		t.Fatal("refill is wrong")
	}
	now = now.Add(time.Hour)
	l.allow("c")
	if len(l.buckets) != 1 {
		t.Fatalf("stale buckets kept: %d", len(l.buckets))
	}
}
