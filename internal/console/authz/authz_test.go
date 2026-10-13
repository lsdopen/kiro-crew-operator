package authz

import (
	"context"
	"errors"
	"sync"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/lsdopen/kiro-crew-operator/api/v1alpha1"
)

const (
	ns        = "kiro-crews"
	crewAlice = "alice"
	reqAlice  = "r-alice"
)

func scheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}

func account(email string, role v1alpha1.Role, manager string) *v1alpha1.KiroCrewAccount {
	return &v1alpha1.KiroCrewAccount{
		ObjectMeta: metav1.ObjectMeta{Name: AccountName(email)},
		Spec:       v1alpha1.KiroCrewAccountSpec{Email: email, Role: role, Manager: manager},
	}
}

func crew(name, owner, namespace string) *v1alpha1.KiroCrew {
	return &v1alpha1.KiroCrew{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec:       v1alpha1.KiroCrewSpec{Owner: owner},
	}
}

func request(name, requester string) *v1alpha1.KiroCrewRequest {
	return &v1alpha1.KiroCrewRequest{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec:       v1alpha1.KiroCrewRequestSpec{Requester: requester},
	}
}

func newStore(t *testing.T, objs ...client.Object) (*Store, client.Client) {
	t.Helper()
	c := fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(objs...).Build()
	return &Store{Client: c, Live: c, CrewNamespace: ns}, c
}

func countAccounts(t *testing.T, c client.Client) []v1alpha1.KiroCrewAccount {
	t.Helper()
	var l v1alpha1.KiroCrewAccountList
	if err := c.List(context.Background(), &l); err != nil {
		t.Fatal(err)
	}
	return l.Items
}

func TestResolveSignsUpExactlyOnceAsUserWithNoManager(t *testing.T) {
	s, c := newStore(t)
	ctx := context.Background()
	for range 3 {
		a, err := s.Resolve(ctx, "alice@example.com")
		if err != nil {
			t.Fatal(err)
		}
		if a.Spec.Role != v1alpha1.RoleUser || a.Spec.Manager != "" || a.Spec.Email != "alice@example.com" {
			t.Fatalf("account = %+v", a.Spec)
		}
	}
	if got := countAccounts(t, c); len(got) != 1 {
		t.Fatalf("accounts = %d, want 1", len(got))
	}
}

func TestResolveConcurrentFirstLogins(t *testing.T) {
	s, c := newStore(t)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, err := s.Resolve(context.Background(), "alice@example.com"); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if got := countAccounts(t, c); len(got) != 1 {
		t.Fatalf("accounts = %d, want 1", len(got))
	}
}

func TestFindFallsBackToLiveOnCacheMiss(t *testing.T) {
	existing := account("alice@example.com", v1alpha1.RoleManager, "")
	live := fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(existing).Build()
	stale := interceptor.NewClient(live, interceptor.Funcs{
		List: func(_ context.Context, _ client.WithWatch, _ client.ObjectList, _ ...client.ListOption) error {
			return nil
		},
	})
	s := &Store{Client: stale, Live: live, CrewNamespace: ns}
	a, err := s.Find(context.Background(), "Alice@Example.com")
	if err != nil || a == nil || a.Spec.Role != v1alpha1.RoleManager {
		t.Fatalf("Find = %+v, %v; want the live account", a, err)
	}
	if a, err := s.Find(context.Background(), "nobody@example.com"); err != nil || a != nil {
		t.Fatalf("Find(absent) = %+v, %v; want nil, nil", a, err)
	}
}

func TestResolveLosesRaceWithStaleCache(t *testing.T) {
	// The cached List sees nothing, but the account already exists live: Create
	// conflicts and Resolve must return the existing account, not overwrite it.
	existing := account("alice@example.com", v1alpha1.RoleAdmin, "")
	live := fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(existing).Build()
	stale := interceptor.NewClient(live, interceptor.Funcs{
		List: func(_ context.Context, _ client.WithWatch, l client.ObjectList, _ ...client.ListOption) error {
			return nil
		},
	})
	s := &Store{Client: stale, Live: live, CrewNamespace: ns}
	a, err := s.Resolve(context.Background(), "alice@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if a.Spec.Role != v1alpha1.RoleAdmin {
		t.Fatalf("role = %s, want the existing admin role", a.Spec.Role)
	}
	if got := countAccounts(t, live); len(got) != 1 || got[0].Spec.Role != v1alpha1.RoleAdmin {
		t.Fatalf("accounts changed: %+v", got)
	}
}

func TestResolveFindsExistingCaseInsensitivelyAndKeepsRole(t *testing.T) {
	s, c := newStore(t, account("Bob@Example.com", v1alpha1.RoleManager, "boss@example.com"))
	a, err := s.Resolve(context.Background(), "bob@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if a.Spec.Role != v1alpha1.RoleManager || a.Spec.Manager != "boss@example.com" {
		t.Fatalf("existing account was altered: %+v", a.Spec)
	}
	if got := countAccounts(t, c); len(got) != 1 {
		t.Fatalf("accounts = %d, want 1", len(got))
	}
}

func TestResolveCreateErrorIsReturned(t *testing.T) {
	base := fake.NewClientBuilder().WithScheme(scheme(t)).Build()
	c := interceptor.NewClient(base, interceptor.Funcs{
		Create: func(context.Context, client.WithWatch, client.Object, ...client.CreateOption) error {
			return apierrors.NewForbidden(v1alpha1.GroupVersion.WithResource("kirocrewaccounts").GroupResource(), "x", nil)
		},
	})
	s := &Store{Client: c, Live: base, CrewNamespace: ns}
	if _, err := s.Resolve(context.Background(), "alice@example.com"); err == nil {
		t.Fatal("create failure swallowed")
	}
}

func TestFindDoesNotCreate(t *testing.T) {
	s, c := newStore(t)
	a, err := s.Find(context.Background(), "ghost@example.com")
	if err != nil || a != nil {
		t.Fatalf("Find = %v, %v", a, err)
	}
	if got := countAccounts(t, c); len(got) != 0 {
		t.Fatalf("Find created an account: %+v", got)
	}
}

func TestAccountName(t *testing.T) {
	a, b := AccountName("Alice.Smith@Example.com"), AccountName("alice.smith@example.com")
	if a != b {
		t.Errorf("not case-insensitive: %s vs %s", a, b)
	}
	if AccountName("alice@example.com") == AccountName("alice@example.org") {
		t.Error("different addresses collided")
	}
	for _, e := range []string{"alice@example.com", "a.very.long.local.part.that.goes.on.and.on.and.on@example.com",
		"+++@example.com", "ÜBER@example.com"} {
		n := AccountName(e)
		if len(n) > 63 || n == "" || n[0] == '-' || n[len(n)-1] == '-' {
			t.Errorf("bad name %q for %q", n, e)
		}
	}
}

func names(v *View) (crews, reqs []string) {
	for i := range v.Crews {
		crews = append(crews, v.Crews[i].Name)
	}
	for i := range v.Requests {
		reqs = append(reqs, v.Requests[i].Name)
	}
	return crews, reqs
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	have := map[string]int{}
	for _, s := range a {
		have[s]++
	}
	for _, s := range b {
		have[s]--
	}
	for _, n := range have {
		if n != 0 {
			return false
		}
	}
	return true
}

func TestVisibleIsRoleScoped(t *testing.T) {
	alice := account("alice@example.com", v1alpha1.RoleUser, "mgr@example.com")
	bob := account("bob@example.com", v1alpha1.RoleUser, "mgr@example.com")
	carol := account("carol@example.com", v1alpha1.RoleUser, "other@example.com")
	mgr := account("mgr@example.com", v1alpha1.RoleManager, "")
	root := account("root@example.com", v1alpha1.RoleAdmin, "")
	s, _ := newStore(t, alice, bob, carol, mgr, root,
		crew(crewAlice, "alice@example.com", ns), crew("bob", "Bob@Example.com", ns),
		crew("carol", "carol@example.com", ns), crew("stray", "alice@example.com", "elsewhere"),
		request(reqAlice, "alice@example.com"), request("r-bob", "bob@example.com"),
		request("r-carol", "carol@example.com"))

	tests := []struct {
		name     string
		who      *v1alpha1.KiroCrewAccount
		crews    []string
		requests []string
		accounts int
	}{
		{"user sees only own", alice, []string{crewAlice}, []string{reqAlice}, 0},
		{"another user cannot see alice's", carol, []string{"carol"}, []string{"r-carol"}, 0},
		{"manager sees reports", mgr, []string{"alice", "bob"}, []string{reqAlice, "r-bob"}, 0},
		{"admin sees all crews in the namespace", root, []string{"alice", "bob", "carol"},
			[]string{reqAlice, "r-bob", "r-carol"}, 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, err := s.Visible(context.Background(), tt.who)
			if err != nil {
				t.Fatal(err)
			}
			crews, reqs := names(v)
			if !equal(crews, tt.crews) || !equal(reqs, tt.requests) || len(v.Accounts) != tt.accounts {
				t.Fatalf("crews=%v requests=%v accounts=%d", crews, reqs, len(v.Accounts))
			}
		})
	}
}

func TestUserWhoClaimsToManageDoesNotGainReports(t *testing.T) {
	// Only role=manager widens the view; a user named as someone's manager does not.
	user := account("alice@example.com", v1alpha1.RoleUser, "")
	report := account("bob@example.com", v1alpha1.RoleUser, "alice@example.com")
	s, _ := newStore(t, user, report, crew("bob", "bob@example.com", ns))
	v, err := s.Visible(context.Background(), user)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Crews) != 0 {
		t.Fatalf("user saw %d crews", len(v.Crews))
	}
}

func TestDeleteAccountRefusesSelf(t *testing.T) {
	root, other := account("root@example.com", v1alpha1.RoleAdmin, ""), account("other@example.com", v1alpha1.RoleAdmin, "")
	s, c := newStore(t, root, other)
	var invalid *InvalidError
	if err := s.DeleteAccount(context.Background(), root, root.Name); !errors.As(err, &invalid) {
		t.Fatalf("err = %v", err)
	}
	if len(countAccounts(t, c)) != 2 {
		t.Fatal("account was deleted")
	}
}
