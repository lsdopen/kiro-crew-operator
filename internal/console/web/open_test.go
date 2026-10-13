package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/lsdopen/kiro-crew-operator/api/v1alpha1"
	"github.com/lsdopen/kiro-crew-operator/internal/console/broker"
)

const (
	tailnet  = "example-tailnet.ts.net"
	aliceURL = "https://alice." + tailnet
	pathOpen = "/crews/alice/open"
	secret   = "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJhbGljZSJ9.c2lnbmF0dXJl"
)

type fakeExec struct {
	out   string
	err   error
	calls int
}

func (f *fakeExec) Exec(context.Context, string, string, string, []string) ([]byte, error) {
	f.calls++
	return []byte(f.out), f.err
}

func readyPod() *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "kiro-crew-alice-0", Namespace: ns, Labels: map[string]string{
			"app.kubernetes.io/name": "kiro-crew", "app.kubernetes.io/instance": "alice",
		}},
		Status: corev1.PodStatus{
			Phase:      corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
		},
	}
}

// openEnv has alice's crew at dashboardURL and a broker over a fake exec.
func openEnv(t *testing.T, dashboardURL string, ex *fakeExec, extra ...client.Object) *env {
	t.Helper()
	c := crewWith("alice", alice, dashboardURL, "")
	e := newEnv(t, []string{testDomain}, append(people(acct(mgr2, v1alpha1.RoleManager, ""), c), extra...)...)
	e.rebuild(&broker.Broker{Pods: e.k8s, Exec: ex, TailnetDomain: tailnet})
	return e
}

// rebuild swaps in a broker over the env's own client.
func (e *env) rebuild(b Opener) {
	e.t.Helper()
	e.srv.Broker = b
}

func goodExec() *fakeExec {
	return &fakeExec{out: "http://localhost:8080?token=loopback\n\n" + aliceURL + "/?token=" + secret + "\n"}
}

func TestOpenRedirectsOwnerWithExactLocationAndHeaders(t *testing.T) {
	ex := goodExec()
	e := openEnv(t, aliceURL, ex, readyPod())
	w := e.post(alice, pathOpen, url.Values{})
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d", w.Code)
	}
	if got, want := w.Header().Get("Location"), aliceURL+"/?token="+secret; got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}
	if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Errorf("headers = %v", w.Header())
	}
	if w.Body.Len() != 0 {
		t.Errorf("body = %q", w.Body.String())
	}
	if ex.calls != 1 {
		t.Errorf("exec calls = %d", ex.calls)
	}
	assertTokenNotLogged(t, e)
}

func TestOpenIsOwnerOnly(t *testing.T) {
	for _, who := range []string{mgr, root, bob, mgr2} {
		t.Run(who, func(t *testing.T) {
			ex := goodExec()
			e := openEnv(t, aliceURL, ex, readyPod())
			w := e.post(who, pathOpen, url.Values{})
			if w.Code != http.StatusForbidden || w.Header().Get("Location") != "" || ex.calls != 0 {
				t.Fatalf("status = %d, Location = %q, exec calls = %d", w.Code, w.Header().Get("Location"), ex.calls)
			}
			assertTokenNotLogged(t, e)
		})
	}
}

func TestOpenRefusals(t *testing.T) {
	tests := []struct {
		name string
		url  string
		pods []client.Object
		code int
	}{
		{"non-https url", "http://alice." + tailnet, []client.Object{readyPod()}, http.StatusBadGateway},
		{"wrong host", "https://alice.evil.example.com", []client.Object{readyPod()}, http.StatusBadGateway},
		{"domain as a prefix", aliceURL + ".evil.example.com", []client.Object{readyPod()}, http.StatusBadGateway},
		{"missing pod", aliceURL, nil, http.StatusConflict},
		{"no dashboard url yet", "", []client.Object{readyPod()}, http.StatusConflict},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ex := goodExec()
			e := openEnv(t, tt.url, ex, tt.pods...)
			w := e.post(alice, pathOpen, url.Values{})
			if w.Code != tt.code || w.Header().Get("Location") != "" || ex.calls != 0 {
				t.Fatalf("status = %d, Location = %q, exec calls = %d", w.Code, w.Header().Get("Location"), ex.calls)
			}
			assertTokenNotLogged(t, e)
		})
	}
}

func TestOpenFailureNeverLogsOrReturnsTheToken(t *testing.T) {
	tests := map[string]*fakeExec{
		"exec error with output": {out: aliceURL + "/?token=" + secret, err: errors.New("stream closed")},
		"no link for the host":   {out: "https://bob." + tailnet + "/?token=" + secret},
	}
	for name, ex := range tests {
		t.Run(name, func(t *testing.T) {
			e := openEnv(t, aliceURL, ex, readyPod())
			w := e.post(alice, pathOpen, url.Values{})
			if w.Code != http.StatusBadGateway || w.Header().Get("Location") != "" {
				t.Fatalf("status = %d, Location = %q", w.Code, w.Header().Get("Location"))
			}
			if strings.Contains(w.Body.String(), secret) || strings.Contains(headersOf(w), secret) {
				t.Error("token in the response")
			}
			if e.logs.Len() == 0 {
				t.Error("failure was not logged at all")
			}
			assertTokenNotLogged(t, e)
		})
	}
}

func TestOpenNeedsCSRFAndPost(t *testing.T) {
	ex := goodExec()
	e := openEnv(t, aliceURL, ex, readyPod())
	c, _ := e.stableSession(alice)
	if w := e.do(http.MethodPost, pathOpen, []*http.Cookie{c}, nil, ""); w.Code != http.StatusForbidden {
		t.Errorf("no CSRF = %d", w.Code)
	}
	if w := e.do(http.MethodPost, pathOpen, []*http.Cookie{c}, map[string]string{csrfHeader: badToken}, ""); w.Code != http.StatusForbidden {
		t.Errorf("bad CSRF = %d", w.Code)
	}
	if w := e.do(http.MethodGet, pathOpen, []*http.Cookie{c}, nil, ""); w.Code != http.StatusMethodNotAllowed || w.Header().Get("Location") != "" {
		t.Errorf("GET = %d", w.Code)
	}
	if ex.calls != 0 {
		t.Fatalf("exec ran %d times", ex.calls)
	}
}

func TestOpenIsRateLimitedPerSession(t *testing.T) {
	ex := goodExec()
	e := openEnv(t, aliceURL, ex, readyPod())
	for i := range openBurst {
		if w := e.post(alice, pathOpen, url.Values{}); w.Code != http.StatusSeeOther {
			t.Fatalf("open %d = %d", i, w.Code)
		}
	}
	if w := e.post(alice, pathOpen, url.Values{}); w.Code != http.StatusTooManyRequests || w.Header().Get("Location") != "" {
		t.Fatalf("after the burst = %d", w.Code)
	}
	if ex.calls != openBurst {
		t.Fatalf("exec calls = %d", ex.calls)
	}
}

func TestOpenWithoutBrokerIsUnavailable(t *testing.T) {
	e := newEnv(t, []string{testDomain}, people(crewWith("alice", alice, aliceURL, ""))...)
	if w := e.post(alice, pathOpen, url.Values{}); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", w.Code)
	}
	c, _ := e.sessionFor(alice)
	if body := e.do(http.MethodGet, "/crews", []*http.Cookie{c}, nil, "").Body.String(); strings.Contains(body, pathOpen) {
		t.Error("Open button shown with brokering off")
	}
}

func TestOnlyTheOwnerGetsAnOpenButton(t *testing.T) {
	e := openEnv(t, aliceURL, goodExec(), readyPod())
	page := func(email string) string {
		c, _ := e.sessionFor(email)
		return e.do(http.MethodGet, "/crews", []*http.Cookie{c}, nil, "").Body.String()
	}
	if !strings.Contains(page(alice), `action="`+pathOpen+`"`) {
		t.Error("owner has no Open button")
	}
	for _, who := range []string{mgr, root} {
		if strings.Contains(page(who), pathOpen) {
			t.Errorf("%s gets an Open button", who)
		}
	}
}

// assertTokenNotLogged checks every captured log line, not just the last.
func assertTokenNotLogged(t *testing.T, e *env) {
	t.Helper()
	if strings.Contains(e.logs.String(), secret) || strings.Contains(e.logs.String(), "token=") {
		t.Errorf("token in logs: %s", e.logs.String())
	}
}

func headersOf(w *httptest.ResponseRecorder) string {
	var b strings.Builder
	for k, v := range w.Header() {
		b.WriteString(k + ": " + strings.Join(v, ",") + "\n")
	}
	return b.String()
}
