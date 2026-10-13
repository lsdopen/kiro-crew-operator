package authz

import (
	"context"
	"errors"
	"testing"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/lsdopen/kiro-crew-operator/api/v1alpha1"
)

const (
	mgrMail   = "mgr@example.com"
	rootMail  = "root@example.com"
	sizePower = "power"
)

func TestPatchCrewNeverWritesSizeOrIdentity(t *testing.T) {
	c := crew(crewAlice, aliceMail, ns)
	s, k := newStore(t, c)
	for _, field := range []string{"size", "owner", "gateway", "storage", "aws"} {
		if err := s.patchCrew(context.Background(), c, map[string]any{field: "x"}); err == nil {
			t.Errorf("%s: patch accepted", field)
		}
	}
	got := &v1alpha1.KiroCrew{}
	if err := k.Get(context.Background(), client.ObjectKeyFromObject(c), got); err != nil || got.Spec.Owner != aliceMail {
		t.Fatalf("crew = %+v %v", got.Spec, err)
	}
}

func TestRequestResizeWho(t *testing.T) {
	alice := account(aliceMail, v1alpha1.RoleUser, mgrMail)
	mgr := account(mgrMail, v1alpha1.RoleManager, "")
	other := account("other@example.com", v1alpha1.RoleManager, "")
	root := account(rootMail, v1alpha1.RoleAdmin, "")
	bob := account("bob@example.com", v1alpha1.RoleUser, mgrMail)
	tests := []struct {
		name     string
		me       *v1alpha1.KiroCrewAccount
		decided  bool
		forbid   bool
		requests string
	}{
		{"owner waits for approval", alice, false, false, aliceMail},
		{"owner's manager approves in one action", mgr, true, false, aliceMail},
		{"admin approves in one action", root, true, false, aliceMail},
		{"another manager is refused", other, false, true, ""},
		{"another user is refused", bob, false, true, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := crew(crewAlice, aliceMail, ns)
			c.Spec.Size = v1alpha1.SizeLight
			s, k := newStore(t, alice, mgr, other, root, bob, c)
			q, err := s.RequestResize(context.Background(), tt.me, crewAlice, sizePower, " more ")
			if tt.forbid {
				if !errors.Is(err, ErrForbidden) {
					t.Fatalf("err = %v", err)
				}
				var l v1alpha1.KiroCrewRequestList
				if k.List(context.Background(), &l) != nil || len(l.Items) != 0 {
					t.Fatalf("a refusal wrote %d requests", len(l.Items))
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			sp := q.Spec
			if sp.Requester != tt.requests || sp.CrewRef != crewAlice || sp.Size != v1alpha1.SizePower || sp.Schedule != nil || sp.Reason != "more" {
				t.Fatalf("spec = %+v", sp)
			}
			if tt.decided != (sp.Decision != nil) || tt.decided && (sp.Decision.By != tt.me.Spec.Email || sp.Decision.Verdict != v1alpha1.VerdictApproved) {
				t.Fatalf("decision = %+v", sp.Decision)
			}
			got := &v1alpha1.KiroCrew{}
			if err := k.Get(context.Background(), client.ObjectKeyFromObject(c), got); err != nil || got.Spec.Size != v1alpha1.SizeLight {
				t.Fatalf("the console changed the crew's size: %+v %v", got.Spec, err)
			}
		})
	}
}

func TestRequestResizeRefusals(t *testing.T) {
	mk := func(mutate func(*v1alpha1.KiroCrew)) *v1alpha1.KiroCrew {
		c := crew(crewAlice, aliceMail, ns)
		c.Spec.Size = v1alpha1.SizeLight
		if mutate != nil {
			mutate(c)
		}
		return c
	}
	alice := account(aliceMail, v1alpha1.RoleUser, "")
	open := request("open", aliceMail)
	allowOnly := &v1alpha1.KiroCrewConfig{
		ObjectMeta: metav1.ObjectMeta{Name: v1alpha1.ConfigName},
		Spec:       v1alpha1.KiroCrewConfigSpec{AllowedSizes: []v1alpha1.Size{v1alpha1.SizeLight, v1alpha1.SizeMini}, DefaultSize: v1alpha1.SizeLight},
	}
	pinnedDisk := resource.MustParse("200Gi")
	tests := []struct {
		name string
		size string
		objs []client.Object
		want error
	}{
		{"same size", "light", []client.Object{alice, mk(nil)}, nil},
		{"unknown size", "xl", []client.Object{alice, mk(nil)}, nil},
		{"size not allowed", sizePower, []client.Object{alice, mk(nil), allowOnly}, nil},
		{"pinned resources", sizePower, []client.Object{alice, mk(func(c *v1alpha1.KiroCrew) { c.Spec.Storage.Size = &pinnedDisk })}, nil},
		{"open request", sizePower, []client.Object{alice, mk(nil), open}, ErrOpenRequest},
		{"missing crew", sizePower, []client.Object{alice}, ErrCrewNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, k := newStore(t, tt.objs...)
			_, err := s.RequestResize(context.Background(), alice, crewAlice, tt.size, "")
			var inv *InvalidError
			if tt.want != nil && !errors.Is(err, tt.want) || tt.want == nil && !errors.As(err, &inv) {
				t.Fatalf("err = %v", err)
			}
			var l v1alpha1.KiroCrewRequestList
			if err := k.List(context.Background(), &l); err != nil || len(l.Items) != requestsIn(tt.objs) {
				t.Fatalf("requests = %d %v", len(l.Items), err)
			}
		})
	}
}

// requestsIn counts the requests already in objs, which a refusal must not add to.
func requestsIn(objs []client.Object) int {
	n := 0
	for _, o := range objs {
		if _, ok := o.(*v1alpha1.KiroCrewRequest); ok {
			n++
		}
	}
	return n
}
