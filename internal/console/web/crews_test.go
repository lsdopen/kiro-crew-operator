package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/lsdopen/kiro-crew-operator/api/v1alpha1"
)

const (
	mgr2      = "mgr2@example.com"
	pathStart = "/crews/alice/start"
	pathStop  = "/crews/alice/stop"
)

func aliceCrew() *v1alpha1.KiroCrew {
	c := crewWith("alice", alice, "", "")
	c.Spec.Size = v1alpha1.SizePower
	return c
}

func crewPeople() []client.Object {
	return people(acct(mgr2, v1alpha1.RoleManager, ""), aliceCrew())
}

func (e *env) theCrew() *v1alpha1.KiroCrew {
	e.t.Helper()
	c := &v1alpha1.KiroCrew{}
	if err := e.k8s.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: crewAliceKey}, c); err != nil {
		e.t.Fatal(err)
	}
	return c
}

func TestStartStopAuthzMatrix(t *testing.T) {
	tests := []struct {
		who     string
		allowed bool
	}{
		{alice, true},                 // owner
		{mgr, true},                   // owner's manager
		{root, true},                  // admin
		{mgr2, false},                 // a manager, but not alice's
		{bob, false},                  // another user
		{"orphan@example.com", false}, // a user with no manager
	}
	for _, tt := range tests {
		for _, path := range []string{pathStart, pathStop} {
			t.Run(tt.who+path, func(t *testing.T) {
				e := newEnv(t, []string{testDomain}, crewPeople()...)
				w := e.post(tt.who, path, url.Values{})
				if tt.allowed && w.Code != http.StatusSeeOther || !tt.allowed && w.Code != http.StatusForbidden {
					t.Fatalf("status = %d", w.Code)
				}
				if got := len(e.patches) == 1; got != tt.allowed {
					t.Fatalf("patches = %d, allowed = %v", len(e.patches), tt.allowed)
				}
			})
		}
	}
}

func TestStartPatchesOnlySuspendAndStartedAt(t *testing.T) {
	e := newEnv(t, []string{testDomain}, crewPeople()...)
	before := time.Now().Add(-time.Second)
	// Identity and spec fields in the form must be ignored.
	w := e.post(alice, pathStart, url.Values{"owner": {bob}, "size": {"mini"}, "suspend": {htmxOn}, "startedAt": {"2020-01-01T00:00:00Z"}})
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != crewsPath {
		t.Fatalf("status = %d %s", w.Code, w.Header().Get("Location"))
	}
	var patch map[string]map[string]any
	if len(e.patches) != 1 || json.Unmarshal(e.patches[0], &patch) != nil {
		t.Fatalf("patches = %q", e.patches)
	}
	spec := patch["spec"]
	if len(patch) != 1 || len(spec) != 2 || spec["suspend"] != false || spec["startedAt"] == nil {
		t.Fatalf("patch = %s", e.patches[0])
	}

	got := e.theCrew()
	if got.Spec.Owner != alice || got.Spec.Size != v1alpha1.SizePower || got.Spec.Suspend || got.Spec.Schedule == nil {
		t.Fatalf("spec = %+v", got.Spec)
	}
	if got.Spec.StartedAt == nil || got.Spec.StartedAt.Time.Before(before) || got.Spec.StartedAt.After(time.Now().Add(time.Second)) {
		t.Fatalf("startedAt = %v", got.Spec.StartedAt)
	}
}

func TestStopPatchesOnlySuspend(t *testing.T) {
	c := aliceCrew()
	c.Spec.StartedAt = &metav1.Time{Time: time.Date(2026, 10, 12, 19, 0, 0, 0, time.UTC)}
	e := newEnv(t, []string{testDomain}, people(c)...)
	if w := e.post(mgr, pathStop, url.Values{"suspend": {"false"}}); w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d", w.Code)
	}
	if len(e.patches) != 1 || string(e.patches[0]) != `{"spec":{"suspend":true}}` {
		t.Fatalf("patches = %q", e.patches)
	}
	got := e.theCrew()
	if !got.Spec.Suspend || got.Spec.StartedAt == nil || got.Spec.Owner != alice {
		t.Fatalf("spec = %+v", got.Spec)
	}
}

func TestStartStopNeedCSRFAndASession(t *testing.T) {
	e := newEnv(t, []string{testDomain}, crewPeople()...)
	c, _ := e.stableSession(alice)
	if w := e.do(http.MethodPost, pathStop, []*http.Cookie{c}, map[string]string{csrfHeader: badToken}, ""); w.Code != http.StatusForbidden {
		t.Errorf("bad CSRF = %d", w.Code)
	}
	if w := e.do(http.MethodPost, pathStop, []*http.Cookie{c}, nil, ""); w.Code != http.StatusForbidden {
		t.Errorf("no CSRF = %d", w.Code)
	}
	if w := e.do(http.MethodPost, pathStop, nil, nil, ""); w.Code != http.StatusFound {
		t.Errorf("no session = %d", w.Code)
	}
	if w := e.post("stranger@example.com", pathStop, url.Values{}); w.Code != http.StatusFound {
		t.Errorf("session without an account = %d", w.Code)
	}
	if w := e.do(http.MethodGet, pathStop, []*http.Cookie{c}, nil, ""); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET = %d", w.Code)
	}
	if len(e.patches) != 0 {
		t.Fatalf("patches = %q", e.patches)
	}
}

func TestStartMissingCrewIs404AndWritesNothing(t *testing.T) {
	e := newEnv(t, []string{testDomain}, crewPeople()...)
	if w := e.post(root, "/crews/nope/start", url.Values{}); w.Code != http.StatusNotFound {
		t.Fatalf("status = %d", w.Code)
	}
	if len(e.patches) != 0 {
		t.Fatalf("patches = %q", e.patches)
	}
}

func TestStartStopWritesAreRateLimited(t *testing.T) {
	e := newEnv(t, []string{testDomain}, crewPeople()...)
	var last int
	for range writeBurst + 1 {
		last = e.post(alice, pathStop, url.Values{}).Code
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("status after burst = %d", last)
	}
}

func TestHTMXStartRendersCrewsWithNotice(t *testing.T) {
	e := newEnv(t, []string{testDomain}, crewPeople()...)
	w := e.post(alice, pathStart, url.Values{}, hdrHX, htmxOn)
	body := w.Body.String()
	if w.Code != http.StatusOK || strings.Contains(body, "<html") || !strings.Contains(body, "Crew starting.") ||
		!strings.Contains(body, `hx-post="/crews/alice/stop"`) {
		t.Fatalf("status = %d body = %s", w.Code, body)
	}
}

func TestOnlyControllersSeeStartAndStop(t *testing.T) {
	e := newEnv(t, []string{testDomain}, crewPeople()...)
	page := func(email string) string {
		c, _ := e.sessionFor(email)
		return e.do(http.MethodGet, "/crews", []*http.Cookie{c}, nil, "").Body.String()
	}
	for _, who := range []string{alice, mgr, root} {
		if !strings.Contains(page(who), `hx-post="/crews/alice/start"`) {
			t.Errorf("%s does not get Start", who)
		}
	}
	if strings.Contains(page(mgr2), "/crews/alice/") {
		t.Error("an unrelated manager sees the crew or its buttons")
	}
}
