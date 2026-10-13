package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/lsdopen/kiro-crew-operator/api/v1alpha1"
	"github.com/lsdopen/kiro-crew-operator/internal/console/authz"
)

const (
	pathConfig  = "/config"
	pathAccount = "/accounts/"
	keyRole     = "role"
	keyManager  = "manager"
	keyAllowed  = "allowed"
	keyDefault  = "default_size"
	keyGateway  = "gateway_image"
	gatewayV1   = "registry.example.com/gateway:1.2.3"
	valMini     = "mini"
	valPower    = "power"
	valUser     = "user"
	valHuge     = "huge"
	keyEmail    = "email"
)

func accountOf(email string) string { return pathAccount + authz.AccountName(email) }

func (e *env) get(email, path string) *httptest.ResponseRecorder {
	e.t.Helper()
	c, _ := e.stableSession(email)
	return e.do(http.MethodGet, path, []*http.Cookie{c}, nil, "")
}

func (e *env) account(email string) *v1alpha1.KiroCrewAccount {
	e.t.Helper()
	a := &v1alpha1.KiroCrewAccount{}
	if err := e.k8s.Get(context.Background(), client.ObjectKey{Name: authz.AccountName(email)}, a); err != nil {
		e.t.Fatal(err)
	}
	return a
}

func (e *env) hasAccount(email string) bool {
	e.t.Helper()
	for _, a := range e.accounts() {
		if a.Spec.Email == email {
			return true
		}
	}
	return false
}

func configObj(spec v1alpha1.KiroCrewConfigSpec) *v1alpha1.KiroCrewConfig {
	return &v1alpha1.KiroCrewConfig{ObjectMeta: metav1.ObjectMeta{Name: v1alpha1.ConfigName}, Spec: spec}
}

func TestAccountWritesAreAdminOnly(t *testing.T) {
	e := newEnv(t, []string{testDomain}, people()...)
	for _, who := range []string{alice, mgr} {
		for _, path := range []string{accountOf(bob), accountOf(bob) + "/delete"} {
			if w := e.post(who, path, url.Values{keyRole: {"admin"}}); w.Code != http.StatusForbidden {
				t.Fatalf("%s %s: status = %d", who, path, w.Code)
			}
		}
		if w := e.post(who, pathConfig, url.Values{keyAllowed: {valMini}, keyDefault: {valMini}}); w.Code != http.StatusForbidden {
			t.Fatalf("%s config: status = %d", who, w.Code)
		}
		if w := e.get(who, pathConfig); w.Code != http.StatusForbidden {
			t.Fatalf("%s GET config: status = %d", who, w.Code)
		}
	}
	if got := e.account(bob); got.Spec.Role != v1alpha1.RoleUser || got.Spec.Manager != "other@example.com" {
		t.Fatalf("bob changed: %+v", got.Spec)
	}
}

func TestAdminEditsRoleAndManager(t *testing.T) {
	e := newEnv(t, []string{testDomain}, people()...)
	w := e.post(root, accountOf(bob), url.Values{keyRole: {"manager"}, keyManager: {" " + mgr + " "}, keyEmail: {"evil@example.com"}})
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/accounts" {
		t.Fatalf("status = %d %s", w.Code, w.Header().Get("Location"))
	}
	if got := e.account(bob).Spec; got.Role != v1alpha1.RoleManager || got.Manager != mgr || got.Email != bob {
		t.Fatalf("spec = %+v", got)
	}
	if w := e.post(root, accountOf(bob), url.Values{keyRole: {valUser}, keyManager: {""}}); w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d", w.Code)
	}
	if got := e.account(bob).Spec; got.Role != v1alpha1.RoleUser || got.Manager != "" {
		t.Fatalf("manager not cleared: %+v", got)
	}
}

func TestAccountEditRefusals(t *testing.T) {
	e := newEnv(t, []string{testDomain}, people()...)
	for name, form := range map[string]url.Values{
		"unknown role":     {keyRole: {"owner"}},
		"empty role":       {keyManager: {mgr}},
		"bad manager":      {keyRole: {valUser}, keyManager: {"not-an-email"}},
		"non-ascii":        {keyRole: {valUser}, keyManager: {"m\u212Aa@example.com"}},
		"manager is self":  {keyRole: {valUser}, keyManager: {"BOB@example.com"}},
		"manager is space": {keyRole: {valUser}, keyManager: {"a b@example.com"}},
	} {
		if w := e.post(root, accountOf(bob), form); w.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d", name, w.Code)
		}
	}
	if w := e.post(root, pathAccount+"missing", url.Values{keyRole: {valUser}}); w.Code != http.StatusNotFound {
		t.Fatalf("missing: status = %d", w.Code)
	}
}

func TestLastAdminCannotBeDemotedOrDeleted(t *testing.T) {
	e := newEnv(t, []string{testDomain}, people()...)
	if w := e.post(root, accountOf(root), url.Values{keyRole: {valUser}}); w.Code != http.StatusConflict {
		t.Fatalf("demote: status = %d", w.Code)
	}
	if w := e.post(root, accountOf(root)+"/delete", nil); w.Code != http.StatusBadRequest {
		t.Fatalf("delete: status = %d", w.Code)
	}
	if got := e.account(root).Spec.Role; got != v1alpha1.RoleAdmin {
		t.Fatalf("role = %s", got)
	}
	// Keeping the role while changing the manager is not a demotion.
	if w := e.post(root, accountOf(root), url.Values{keyRole: {"admin"}, keyManager: {mgr}}); w.Code != http.StatusSeeOther {
		t.Fatalf("edit: status = %d", w.Code)
	}
}

func TestAdminCanLeaveWhileAnotherAdminRemains(t *testing.T) {
	second := "second@example.com"
	e := newEnv(t, []string{testDomain}, people(acct(second, v1alpha1.RoleAdmin, ""))...)
	if w := e.post(root, accountOf(root), url.Values{keyRole: {valUser}}); w.Code != http.StatusSeeOther {
		t.Fatalf("demote: status = %d", w.Code)
	}
	// root is now a user: the last admin is second.
	if w := e.post(second, accountOf(second)+"/delete", nil); w.Code != http.StatusBadRequest {
		t.Fatalf("delete self: status = %d", w.Code)
	}
	if w := e.post(second, accountOf(bob)+"/delete", nil); w.Code != http.StatusSeeOther {
		t.Fatalf("delete bob: status = %d", w.Code)
	}
	if e.hasAccount(bob) {
		t.Fatal("bob still exists")
	}
	if w := e.post(second, accountOf(bob)+"/delete", nil); w.Code != http.StatusNotFound {
		t.Fatalf("delete again: status = %d", w.Code)
	}
}

func TestDemotedAdminIsNotRenderedAsAdmin(t *testing.T) {
	second := "second@example.com"
	e := newEnv(t, []string{testDomain}, people(acct(second, v1alpha1.RoleAdmin, ""))...)
	w := e.post(root, accountOf(root), url.Values{keyRole: {valUser}}, "HX-Request", "true")
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), accountOf(bob)+"/delete") {
		t.Fatalf("status = %d, admin view rendered: %s", w.Code, w.Body.String())
	}
}

func TestDeletedAccountSessionIsRejected(t *testing.T) {
	e := newEnv(t, []string{testDomain}, people()...)
	if w := e.post(root, accountOf(bob)+"/delete", nil); w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d", w.Code)
	}
	if w := e.get(bob, "/crews"); w.Code != http.StatusFound {
		t.Fatalf("deleted account still signed in: %d", w.Code)
	}
}

func TestAccountsPageOffersEditsToAdmins(t *testing.T) {
	e := newEnv(t, []string{testDomain}, people()...)
	body := e.get(root, "/accounts").Body.String()
	for _, want := range []string{accountOf(bob), accountOf(bob) + "/delete", `name="role"`, `name="manager"`, "/config"} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in %s", want, body)
		}
	}
}

func TestConfigEditorSavesAndValidates(t *testing.T) {
	e := newEnv(t, []string{testDomain}, people(configObj(v1alpha1.KiroCrewConfigSpec{}))...)
	w := e.post(root, pathConfig, url.Values{
		keyAllowed: {valMini, "light", valMini}, keyDefault: {valMini}, keyGateway: {" " + gatewayV1 + " "},
		"mcp_config_ref": {"shared-mcp"},
	})
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != pathConfig {
		t.Fatalf("status = %d %s", w.Code, w.Header().Get("Location"))
	}
	got := e.config().Spec
	if len(got.AllowedSizes) != 2 || got.DefaultSize != v1alpha1.SizeMini || got.Gateway.Image != gatewayV1 ||
		got.Tailscale.Image != "" || got.MCPConfigRef != "shared-mcp" {
		t.Fatalf("spec = %+v", got)
	}
	for name, form := range map[string]url.Values{
		"none allowed":      {keyDefault: {valMini}},
		"unknown size":      {keyAllowed: {valHuge}, keyDefault: {valHuge}},
		"default not in it": {keyAllowed: {valMini}, keyDefault: {valPower}},
		"image whitespace":  {keyAllowed: {valMini}, keyDefault: {valMini}, keyGateway: {"a b"}},
		"bad configmap":     {keyAllowed: {valMini}, keyDefault: {valMini}, "mcp_config_ref": {"Not_A_Name"}},
	} {
		if w := e.post(root, pathConfig, form); w.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d", name, w.Code)
		}
	}
	if got := e.config().Spec; got.DefaultSize != v1alpha1.SizeMini || len(got.AllowedSizes) != 2 {
		t.Fatalf("refused edits changed the config: %+v", got)
	}
}

func TestConfigEditorNeedsTheOperatorsConfig(t *testing.T) {
	e := newEnv(t, []string{testDomain}, people()...)
	if w := e.post(root, pathConfig, url.Values{keyAllowed: {valMini}, keyDefault: {valMini}}); w.Code != http.StatusNotFound {
		t.Fatalf("status = %d", w.Code)
	}
	if w := e.get(root, pathConfig); w.Code != http.StatusOK {
		t.Fatalf("GET status = %d", w.Code)
	}
}

func (e *env) config() *v1alpha1.KiroCrewConfig {
	e.t.Helper()
	c := &v1alpha1.KiroCrewConfig{}
	if err := e.k8s.Get(context.Background(), client.ObjectKey{Name: v1alpha1.ConfigName}, c); err != nil {
		e.t.Fatal(err)
	}
	return c
}

func TestRequestFormAndWriteHonourAllowedSizes(t *testing.T) {
	cfg := configObj(v1alpha1.KiroCrewConfigSpec{
		AllowedSizes: []v1alpha1.Size{v1alpha1.SizeMini, v1alpha1.SizePower}, DefaultSize: v1alpha1.SizePower,
	})
	e := newEnv(t, []string{testDomain}, people(cfg)...)
	body := e.get(alice, pathRequests).Body.String()
	if !strings.Contains(body, `<option value="power" selected>`) || !strings.Contains(body, `<option value="mini">`) ||
		strings.Contains(body, `value="light"`) || strings.Contains(body, `value="development"`) {
		t.Fatalf("form = %s", body)
	}
	if w := e.post(alice, pathRequests, url.Values{keySize: {sizeLight}}); w.Code != http.StatusBadRequest {
		t.Fatalf("disallowed size: status = %d", w.Code)
	}
	if len(e.requests()) != 0 {
		t.Fatal("a disallowed size was stored")
	}
	if w := e.post(alice, pathRequests, url.Values{keySize: {valPower}}); w.Code != http.StatusSeeOther {
		t.Fatalf("allowed size: status = %d", w.Code)
	}
}

func TestRequestFormDefaultsWithoutConfig(t *testing.T) {
	e := newEnv(t, []string{testDomain}, people()...)
	if body := e.get(alice, pathRequests).Body.String(); !strings.Contains(body, `<option value="light" selected>`) ||
		!strings.Contains(body, `value="power"`) {
		t.Fatalf("form = %s", body)
	}
}
