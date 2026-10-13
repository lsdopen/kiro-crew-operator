package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/lsdopen/kiro-crew-operator/api/v1alpha1"
)

const (
	pathSchedule = "/crews/alice/schedule"
	pathResize   = "/crews/alice/resize"
	pathMCP      = "/crews/alice/mcp"
	keyMCP       = "mcp_config_ref"
	keyUp        = "up"
	mcpTeam      = "team-mcp"
	keyReason    = "reason"
	keyRequester = "requester"
	crewAliceKey = "alice"
	hdrHX        = "HX-Request"
)

func editPeople(extra ...client.Object) []client.Object {
	cfg := configObj(v1alpha1.KiroCrewConfigSpec{MCPConfigs: []string{mcpTeam, "other-mcp"}})
	return append(crewPeople(), append([]client.Object{cfg}, extra...)...)
}

func scheduleForm() url.Values {
	return url.Values{keyUp: {"0 9 * * 1-5"}, keyDown: {"0 17 * * 1-5"}, keyZone: {"Europe/London"}}
}

func TestEditSchedulePatchesOnlyTheSchedule(t *testing.T) {
	tests := []struct {
		who     string
		allowed bool
	}{
		{alice, true}, {mgr, true}, {root, true},
		{mgr2, false}, {bob, false}, {"orphan@example.com", false},
	}
	for _, tt := range tests {
		t.Run(tt.who, func(t *testing.T) {
			e := newEnv(t, []string{testDomain}, editPeople()...)
			form := scheduleForm()
			// Fields outside the schedule must be ignored.
			form.Set("size", "mini")
			form.Set("owner", bob)
			form.Set("suspend", "true")
			w := e.post(tt.who, pathSchedule, form)
			if tt.allowed && w.Code != http.StatusSeeOther || !tt.allowed && w.Code != http.StatusForbidden {
				t.Fatalf("status = %d", w.Code)
			}
			if !tt.allowed {
				if len(e.patches) != 0 {
					t.Fatalf("a refusal wrote %q", e.patches)
				}
				return
			}
			want := `{"spec":{"schedule":{"up":"0 9 * * 1-5","down":"0 17 * * 1-5","timezone":"Europe/London"}}}`
			if len(e.patches) != 1 || string(e.patches[0]) != want {
				t.Fatalf("patches = %q", e.patches)
			}
			got := e.theCrew()
			if got.Spec.Size != v1alpha1.SizePower || got.Spec.Owner != alice || got.Spec.Suspend || got.Spec.Schedule.Up != "0 9 * * 1-5" {
				t.Fatalf("spec = %+v", got.Spec)
			}
		})
	}
}

func TestEditScheduleClearsAndValidates(t *testing.T) {
	e := newEnv(t, []string{testDomain}, editPeople()...)
	if w := e.post(alice, pathSchedule, url.Values{}); w.Code != http.StatusSeeOther {
		t.Fatalf("clear status = %d", w.Code)
	}
	if len(e.patches) != 1 || string(e.patches[0]) != `{"spec":{"schedule":null}}` || e.theCrew().Spec.Schedule != nil {
		t.Fatalf("patches = %q, schedule = %+v", e.patches, e.theCrew().Spec.Schedule)
	}

	e = newEnv(t, []string{testDomain}, editPeople()...)
	for name, form := range map[string]url.Values{
		"bad cron": {keyUp: {"bad cron"}, keyDown: {cronDown}},
		"half":     {keyUp: {cronUp}},
		"bad zone": {keyUp: {cronUp}, keyDown: {cronDown}, keyZone: {"Nowhere/Land"}},
	} {
		if w := e.post(alice, pathSchedule, form); w.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d", name, w.Code)
		}
	}
	if len(e.patches) != 0 {
		t.Fatalf("invalid input wrote %q", e.patches)
	}
}

func TestResizeFilesARequestAndNeverWritesTheCrew(t *testing.T) {
	e := newEnv(t, []string{testDomain}, editPeople()...)
	w := e.post(alice, pathResize, url.Values{
		keySize: {sizeLight}, keyReason: {" smaller "},
		// Identity, decision and target fields must be ignored.
		keyRequester: {bob}, "by": {root}, keyVerdict: {approved}, "crewRef": {"bob"}, "schedule": {cronUp},
	})
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != crewsPath {
		t.Fatalf("status = %d %s", w.Code, w.Header().Get("Location"))
	}
	got := e.requests()
	if len(got) != 1 {
		t.Fatalf("requests = %d", len(got))
	}
	sp := got[0].Spec
	if sp.Requester != alice || sp.CrewRef != crewAliceKey || sp.Size != v1alpha1.SizeLight || sp.Decision != nil ||
		sp.Schedule != nil || sp.Reason != "smaller" {
		t.Fatalf("request = %+v", sp)
	}
	if len(e.patches) != 0 || e.theCrew().Spec.Size != v1alpha1.SizePower {
		t.Fatalf("the console touched the crew: patches %q, size %s", e.patches, e.theCrew().Spec.Size)
	}
}

func TestResizeByTheOwnersManagerOrAnAdminIsApprovedByThem(t *testing.T) {
	for _, who := range []string{mgr, root} {
		t.Run(who, func(t *testing.T) {
			e := newEnv(t, []string{testDomain}, editPeople()...)
			if w := e.post(who, pathResize, url.Values{keySize: {sizeLight}, "by": {alice}}); w.Code != http.StatusSeeOther {
				t.Fatalf("status = %d", w.Code)
			}
			got := e.requests()
			if len(got) != 1 {
				t.Fatalf("requests = %d", len(got))
			}
			sp := got[0].Spec
			if sp.Requester != alice || sp.CrewRef != crewAliceKey || sp.Decision == nil || sp.Decision.By != who ||
				sp.Decision.Verdict != v1alpha1.VerdictApproved {
				t.Fatalf("request = %+v", sp)
			}
			if len(e.patches) != 0 || e.theCrew().Spec.Size != v1alpha1.SizePower {
				t.Fatalf("the console touched the crew: patches %q", e.patches)
			}
		})
	}
}

func TestPendingResizeRowShowsCrewAndWhoFiled(t *testing.T) {
	e := newEnv(t, []string{testDomain}, editPeople()...)
	if w := e.post(mgr, pathResize, url.Values{keySize: {sizeLight}}); w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d", w.Code)
	}
	body := e.get(alice, "/requests").Body.String()
	if !strings.Contains(body, "resize of "+crewAliceKey) || !strings.Contains(body, "filed by "+mgr) {
		t.Fatalf("request row lacks the crew or filer: %s", body)
	}
	q := &v1alpha1.KiroCrewRequest{Spec: v1alpha1.KiroCrewRequestSpec{CrewRef: "spec-crew"}}
	if r := requestRow(q); r.CrewRef != "spec-crew" || !r.Resize {
		t.Fatalf("row = %+v", r)
	}
	q.Status.CrewRef = "status-crew"
	if r := requestRow(q); r.CrewRef != "status-crew" {
		t.Fatalf("row = %+v", r)
	}
}

func TestResizeRefusals(t *testing.T) {
	disk := resource.MustParse("100Gi")
	pinned := aliceCrew()
	pinned.Spec.Storage.Size = &disk
	narrow := configObj(v1alpha1.KiroCrewConfigSpec{
		AllowedSizes: []v1alpha1.Size{v1alpha1.SizeLight}, DefaultSize: v1alpha1.SizeLight,
	})
	tests := []struct {
		name string
		who  string
		form url.Values
		objs []client.Object
		want int
	}{
		{"another manager", mgr2, url.Values{keySize: {sizeLight}}, editPeople(), http.StatusForbidden},
		{"another user", bob, url.Values{keySize: {sizeLight}}, editPeople(), http.StatusForbidden},
		{"missing crew", root, url.Values{keySize: {sizeLight}}, people(configObj(v1alpha1.KiroCrewConfigSpec{})), http.StatusNotFound},
		{"same size", alice, url.Values{keySize: {"power"}}, editPeople(), http.StatusBadRequest},
		{"bad size", alice, url.Values{keySize: {"xl"}}, editPeople(), http.StatusBadRequest},
		{"size not allowed", alice, url.Values{keySize: {valMini}}, people(narrow, aliceCrew()), http.StatusBadRequest},
		{"pinned resources", alice, url.Values{keySize: {sizeLight}}, people(pinned), http.StatusBadRequest},
		{"open request", alice, url.Values{keySize: {sizeLight}}, editPeople(pendingReq(rAlice, alice)), http.StatusConflict},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, []string{testDomain}, tt.objs...)
			before := len(e.requests())
			if w := e.post(tt.who, pathResize, tt.form); w.Code != tt.want {
				t.Fatalf("status = %d, want %d", w.Code, tt.want)
			}
			if len(e.requests()) != before || len(e.patches) != 0 {
				t.Fatalf("a refusal wrote: requests %d, patches %q", len(e.requests()), e.patches)
			}
		})
	}
}

func TestOnlyAdminsChangeMCPConfigFromTheAllowlist(t *testing.T) {
	for _, who := range []string{alice, mgr, mgr2, bob} {
		e := newEnv(t, []string{testDomain}, editPeople()...)
		if w := e.post(who, pathMCP, url.Values{keyMCP: {mcpTeam}}); w.Code != http.StatusForbidden || len(e.patches) != 0 {
			t.Errorf("%s: status = %d, patches = %q", who, w.Code, e.patches)
		}
	}

	e := newEnv(t, []string{testDomain}, editPeople()...)
	if w := e.post(root, pathMCP, url.Values{keyMCP: {mcpTeam}, keySize: {valMini}}); w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d", w.Code)
	}
	if len(e.patches) != 1 || string(e.patches[0]) != `{"spec":{"mcpConfigRef":"`+mcpTeam+`"}}` {
		t.Fatalf("patches = %q", e.patches)
	}
	if got := e.theCrew(); got.Spec.MCPConfigRef != mcpTeam || got.Spec.Size != v1alpha1.SizePower {
		t.Fatalf("spec = %+v", got.Spec)
	}
	if w := e.post(root, pathMCP, url.Values{keyMCP: {"not-listed"}}); w.Code != http.StatusBadRequest {
		t.Fatalf("off-list status = %d", w.Code)
	}
	if w := e.post(root, pathMCP, url.Values{}); w.Code != http.StatusSeeOther || e.theCrew().Spec.MCPConfigRef != "" {
		t.Fatalf("clear status = %d, ref = %q", w.Code, e.theCrew().Spec.MCPConfigRef)
	}
	if w := e.post(root, "/crews/nope/mcp", url.Values{keyMCP: {mcpTeam}}); w.Code != http.StatusNotFound {
		t.Fatalf("missing crew status = %d", w.Code)
	}
}

func TestCrewEditNeedsCSRFAndASession(t *testing.T) {
	e := newEnv(t, []string{testDomain}, editPeople()...)
	c, _ := e.stableSession(root)
	for _, path := range []string{pathSchedule, pathResize, pathMCP} {
		form := url.Values{keySize: {sizeLight}, keyMCP: {mcpTeam}}.Encode()
		if w := e.do(http.MethodPost, path, []*http.Cookie{c}, map[string]string{csrfHeader: badToken}, form); w.Code != http.StatusForbidden {
			t.Errorf("%s bad CSRF = %d", path, w.Code)
		}
		if w := e.do(http.MethodPost, path, []*http.Cookie{c}, nil, form); w.Code != http.StatusForbidden {
			t.Errorf("%s no CSRF = %d", path, w.Code)
		}
		if w := e.do(http.MethodPost, path, nil, nil, form); w.Code != http.StatusFound {
			t.Errorf("%s no session = %d", path, w.Code)
		}
		if w := e.do(http.MethodGet, path, []*http.Cookie{c}, nil, ""); w.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s GET = %d", path, w.Code)
		}
	}
	if len(e.patches) != 0 || len(e.requests()) != 0 {
		t.Fatalf("unauthenticated writes: patches %q, requests %d", e.patches, len(e.requests()))
	}
}

func TestEditFormsAreShownToWhoCanUseThem(t *testing.T) {
	e := newEnv(t, []string{testDomain}, editPeople()...)
	page := func(email string) string {
		c, _ := e.sessionFor(email)
		return e.do(http.MethodGet, "/crews", []*http.Cookie{c}, nil, "").Body.String()
	}
	for _, who := range []string{alice, mgr, root} {
		body := page(who)
		if !strings.Contains(body, `hx-post="`+pathSchedule+`"`) || !strings.Contains(body, `hx-post="`+pathResize+`"`) {
			t.Errorf("%s does not get the schedule and resize forms", who)
		}
		if hasMCP := strings.Contains(body, `hx-post="`+pathMCP+`"`); hasMCP != (who == root) {
			t.Errorf("%s MCP form shown = %v", who, hasMCP)
		}
	}
	if strings.Contains(page(mgr2), "/crews/alice/") {
		t.Error("an unrelated manager sees the crew's forms")
	}
}

func TestConfigEditorKeepsTheMCPAllowlist(t *testing.T) {
	e := newEnv(t, []string{testDomain}, people(configObj(v1alpha1.KiroCrewConfigSpec{}))...)
	form := url.Values{keyAllowed: {valMini}, keyDefault: {valMini}, "mcp_configs": {"team-mcp\r\nother-mcp\nteam-mcp"}}
	if w := e.post(root, pathConfig, form); w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d", w.Code)
	}
	if got := e.config().Spec.MCPConfigs; len(got) != 2 || got[0] != mcpTeam || got[1] != "other-mcp" {
		t.Fatalf("mcpConfigs = %v", got)
	}
	form.Set("mcp_configs", "Not_A_Name")
	if w := e.post(root, pathConfig, form); w.Code != http.StatusBadRequest {
		t.Fatalf("bad name status = %d", w.Code)
	}
}

func TestResizeSelectKeepsCurrentSize(t *testing.T) {
	all := []v1alpha1.Size{v1alpha1.SizeMini, v1alpha1.SizeLight, v1alpha1.SizeDevelopment, v1alpha1.SizePower}
	const mini = "mini — 0.5 CPU · 8Gi RAM · 20Gi disk (current)"
	tests := []struct {
		name    string
		allowed []v1alpha1.Size
		cur     string
		absent  []string
	}{
		{"current size allowed", all, `<option value="mini" selected>` + mini + `</option>`, nil},
		{"current size not allowed", []v1alpha1.Size{v1alpha1.SizeLight, v1alpha1.SizePower}, `<option value="mini" selected disabled>` + mini + `</option>`, []string{`value="development"`}},
	}
	hx := []string{hdrHX, htmxOn}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			crew := aliceCrew()
			crew.Spec.Size = v1alpha1.SizeMini
			cfg := configObj(v1alpha1.KiroCrewConfigSpec{AllowedSizes: tt.allowed, DefaultSize: v1alpha1.SizeLight})
			e := newEnv(t, []string{testDomain}, append(people(acct(mgr2, v1alpha1.RoleManager, "")), crew, cfg)...)
			// The page and every htmx partial that re-renders the crew row.
			bodies := map[string]string{
				"page":     e.get(alice, crewsPath).Body.String(),
				"start":    e.post(alice, "/crews/alice/start", url.Values{}, hx...).Body.String(),
				"stop":     e.post(alice, "/crews/alice/stop", url.Values{}, hx...).Body.String(),
				"schedule": e.post(alice, pathSchedule, scheduleForm(), hx...).Body.String(),
			}
			for from, body := range bodies {
				if !strings.Contains(body, `<select name="size" autocomplete="off">`) || !strings.Contains(body, tt.cur) {
					t.Fatalf("%s: missing %q in %s", from, tt.cur, body)
				}
				for _, no := range tt.absent {
					if strings.Contains(body, no) {
						t.Fatalf("%s: unexpected %q", from, no)
					}
				}
			}
		})
	}
}

func TestSizePickersShowSpecs(t *testing.T) {
	e := newEnv(t, []string{testDomain}, editPeople()...)
	for _, path := range []string{crewsPath, "/requests"} {
		if body := e.get(alice, path).Body.String(); !strings.Contains(body, "light — 1 CPU · 16Gi RAM · 40Gi disk") {
			t.Fatalf("%s lacks size specs: %s", path, body)
		}
	}
	if body := e.get(root, pathConfig).Body.String(); !strings.Contains(body, "power — 4 CPU · 64Gi RAM · 80Gi disk") {
		t.Fatalf("config lacks size specs: %s", body)
	}
}
