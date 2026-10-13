package authz

import (
	"context"
	"errors"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/lsdopen/kiro-crew-operator/api/v1alpha1"
)

const (
	cronUp, cronDown = "0 8 * * 1-5", "0 18 * * 1-5"
	sizeLight        = "light"
)

func TestNewRequestValidate(t *testing.T) {
	ok := NewRequest{Size: sizeLight, Up: cronUp, Down: cronDown, Zone: "Europe/London"}
	size, sched, err := ok.Validate()
	if err != nil || size != v1alpha1.SizeLight || sched == nil || sched.Timezone != "Europe/London" {
		t.Fatalf("valid input: %v %v %v", size, sched, err)
	}
	if _, sched, err := (NewRequest{Size: "mini", Up: cronUp, Down: cronDown}).Validate(); err != nil || sched.Timezone != "UTC" {
		t.Fatalf("default zone: %v %v", sched, err)
	}
	if _, sched, err := (NewRequest{Size: "mini"}).Validate(); err != nil || sched != nil {
		t.Fatalf("no schedule: %v %v", sched, err)
	}
	for name, in := range map[string]NewRequest{
		"size":         {Size: "xl"},
		"cron":         {Size: sizeLight, Up: "bad cron", Down: cronDown},
		"descriptor":   {Size: sizeLight, Up: "@daily", Down: cronDown},
		"never fires":  {Size: sizeLight, Up: "0 0 30 2 *", Down: cronDown},
		"zone":         {Size: sizeLight, Up: cronUp, Down: cronDown, Zone: "Nowhere/Land"},
		"half":         {Size: sizeLight, Down: cronDown},
		"zone no cron": {Size: sizeLight, Zone: "UTC"},
	} {
		var inv *InvalidError
		if _, _, err := in.Validate(); !errors.As(err, &inv) {
			t.Errorf("%s: err = %v, want InvalidError", name, err)
		}
	}
}

func TestCanDecide(t *testing.T) {
	alice := account("alice@example.com", v1alpha1.RoleUser, "mgr@example.com")
	orphan := account("orphan@example.com", v1alpha1.RoleUser, "")
	tests := []struct {
		name      string
		me        *v1alpha1.KiroCrewAccount
		requester *v1alpha1.KiroCrewAccount
		want      bool
	}{
		{"admin", account("root@example.com", v1alpha1.RoleAdmin, ""), alice, true},
		{"admin, no requester account", account("root@example.com", v1alpha1.RoleAdmin, ""), nil, true},
		{"manager of requester", account("MGR@example.com", v1alpha1.RoleManager, ""), alice, true},
		{"other manager", account("other@example.com", v1alpha1.RoleManager, ""), alice, false},
		{"manager, requester has none", account("mgr@example.com", v1alpha1.RoleManager, ""), orphan, false},
		{"manager, no requester account", account("mgr@example.com", v1alpha1.RoleManager, ""), nil, false},
		{"user named manager", account("mgr@example.com", v1alpha1.RoleUser, ""), alice, false},
	}
	for _, tt := range tests {
		if got := CanDecide(tt.me, tt.requester); got != tt.want {
			t.Errorf("%s: got %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestDecideConflictAndNamespace(t *testing.T) {
	mgr := account("mgr@example.com", v1alpha1.RoleManager, "")
	objs := []client.Object{account("alice@example.com", v1alpha1.RoleUser, "mgr@example.com"), mgr, request(reqAlice, "alice@example.com")}

	conflict := fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(objs...).WithInterceptorFuncs(interceptor.Funcs{
		Patch: func(context.Context, client.WithWatch, client.Object, client.Patch, ...client.PatchOption) error {
			return apierrors.NewConflict(schema.GroupResource{Resource: "kirocrewrequests"}, reqAlice, errors.New("stale"))
		},
	}).Build()
	s := &Store{Client: conflict, Live: conflict, CrewNamespace: ns}
	if err := s.Decide(context.Background(), mgr, reqAlice, v1alpha1.VerdictApproved); !errors.Is(err, ErrConflict) {
		t.Errorf("conflict: err = %v", err)
	}

	s, _ = newStore(t, objs...)
	s.CrewNamespace = ""
	if err := s.Decide(context.Background(), mgr, reqAlice, v1alpha1.VerdictApproved); !errors.Is(err, ErrNoNamespace) {
		t.Errorf("no namespace: err = %v", err)
	}
	if _, err := s.CreateRequest(context.Background(), mgr, NewRequest{Size: sizeLight}); !errors.Is(err, ErrNoNamespace) {
		t.Errorf("create without namespace: err = %v", err)
	}
}
