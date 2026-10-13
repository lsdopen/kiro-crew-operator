package auth

import (
	"errors"
	"testing"
)

const (
	aliceEmail    = "alice@example.com"
	exampleDomain = "example.com"
)

func TestCheckClaims(t *testing.T) {
	good := Claims{Email: aliceEmail, EmailVerified: true, HostedDomain: exampleDomain}
	with := func(f func(*Claims)) Claims {
		c := good
		f(&c)
		return c
	}
	tests := []struct {
		name    string
		claims  Claims
		allowed []string
		want    error
		email   string
	}{
		{name: "ok", claims: good, allowed: []string{exampleDomain}, email: aliceEmail},
		{name: "ok, case and spacing normalised", allowed: []string{" Example.COM "},
			claims: with(func(c *Claims) { c.Email = "Alice@Example.com"; c.HostedDomain = "EXAMPLE.com" }),
			email:  aliceEmail},
		{name: "ok, second of several domains", claims: good, allowed: []string{"other.org", exampleDomain},
			email: aliceEmail},
		{name: "wrong hd", claims: with(func(c *Claims) { c.HostedDomain = "evil.org"; c.Email = "a@evil.org" }),
			allowed: []string{exampleDomain}, want: ErrDomainNotAllowed},
		{name: "missing hd", claims: with(func(c *Claims) { c.HostedDomain = "" }),
			allowed: []string{exampleDomain}, want: ErrMissingDomain},
		{name: "email not verified", claims: with(func(c *Claims) { c.EmailVerified = false }),
			allowed: []string{exampleDomain}, want: ErrEmailUnverified},
		{name: "email in another domain than hd", claims: with(func(c *Claims) { c.Email = "alice@evil.org" }),
			allowed: []string{exampleDomain}, want: ErrEmailMismatch},
		{name: "email is a subdomain of hd", claims: with(func(c *Claims) { c.Email = "alice@sub.example.com" }),
			allowed: []string{exampleDomain}, want: ErrEmailMismatch},
		{name: "email smuggles a second @", claims: with(func(c *Claims) { c.Email = "a@evil.org@example.com" }),
			allowed: []string{exampleDomain}, want: ErrEmailMismatch},
		{name: "email has no local part", claims: with(func(c *Claims) { c.Email = "@example.com" }),
			allowed: []string{exampleDomain}, want: ErrEmailMismatch},
		{name: "empty email", claims: with(func(c *Claims) { c.Email = "" }),
			allowed: []string{exampleDomain}, want: ErrEmailMismatch},
		{name: "nil list refuses", claims: good, allowed: nil, want: ErrNoDomains},
		{name: "empty list refuses", claims: good, allowed: []string{}, want: ErrNoDomains},
		{name: "blank entries refuse", claims: good, allowed: []string{"", "  "}, want: ErrNoDomains},
		{name: "blank hd does not match a blank entry",
			claims:  with(func(c *Claims) { c.HostedDomain = "" }),
			allowed: []string{"", exampleDomain}, want: ErrMissingDomain},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			email, err := CheckClaims(tt.claims, tt.allowed)
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
			if email != tt.email {
				t.Fatalf("email = %q, want %q", email, tt.email)
			}
		})
	}
}

func TestValidateDomains(t *testing.T) {
	if err := ValidateDomains(nil, false); err == nil {
		t.Error("empty list started without allowEmpty")
	}
	if err := ValidateDomains([]string{" "}, false); err == nil {
		t.Error("blank list started without allowEmpty")
	}
	if err := ValidateDomains(nil, true); err != nil {
		t.Errorf("allowEmpty should start: %v", err)
	}
	if err := ValidateDomains([]string{exampleDomain}, false); err != nil {
		t.Errorf("valid list refused: %v", err)
	}
}
