package authz

import (
	"testing"

	v1alpha1 "github.com/lsdopen/kiro-crew-operator/api/v1alpha1"
)

const (
	aliceMail = "alice@example.com"
	kateMail  = "kate@example.com"
)

func TestEmailEqualIsASCIIOnly(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		{aliceMail, "Alice@Example.COM", true},
		{aliceMail, "bob@example.com", false},
		{aliceMail, "alice@example.co", false},
		{aliceMail, "", false},
		{"", "", true},
		{aliceMail, "alice\u212a@example.com", false},
		{aliceMail, "\u017flice@example.com", false},
		{kateMail, "\u212aate@example.com", false},
	}
	for _, tt := range tests {
		if got := EmailEqual(tt.a, tt.b); got != tt.want {
			t.Errorf("EmailEqual(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestOwnerAndManagerIdentityIgnoreUnicodeFolding(t *testing.T) {
	crew := &v1alpha1.KiroCrew{Spec: v1alpha1.KiroCrewSpec{Owner: kateMail}}
	lookalike := &v1alpha1.KiroCrewAccount{Spec: v1alpha1.KiroCrewAccountSpec{Email: "\u212aate@example.com"}}
	if IsOwner(lookalike, crew) {
		t.Error("a Kelvin-sign lookalike became the owner")
	}
	lookalike.Spec.Role = v1alpha1.RoleManager
	owner := &v1alpha1.KiroCrewAccount{Spec: v1alpha1.KiroCrewAccountSpec{Email: kateMail, Manager: "m\u017f@example.com"}}
	lookalike.Spec.Email = "ms@example.com"
	if CanControl(lookalike, owner, crew) {
		t.Error("a long-s lookalike became the owner's manager")
	}
	lookalike.Spec.Email = "M\u017f@example.com"
	if !CanControl(lookalike, owner, crew) {
		t.Error("the exact manager email must still control the crew")
	}
}
