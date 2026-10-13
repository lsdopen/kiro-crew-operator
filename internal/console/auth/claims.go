// Package auth holds the console's login primitives: OIDC verification, the ID
// token claim policy, and sealed session cookies.
package auth

import (
	"errors"
	"slices"
	"strings"
)

// Claims is the subset of ID token claims the console's policy looks at.
type Claims struct {
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	// HostedDomain is Google's "hd" claim: the Workspace domain of the account.
	HostedDomain string `json:"hd"`
	Nonce        string `json:"nonce"`
}

// Reasons CheckClaims refuses a login.
var (
	ErrNoDomains        = errors.New("no allowed domains configured")
	ErrEmailUnverified  = errors.New("email is not verified")
	ErrMissingDomain    = errors.New("id token has no hd claim")
	ErrDomainNotAllowed = errors.New("hd is not an allowed domain")
	ErrEmailMismatch    = errors.New("email does not belong to hd")
	ErrEmailNotASCII    = errors.New("email is not ASCII")
)

// NormalizeDomains lowercases, trims and drops empty entries.
func NormalizeDomains(in []string) []string {
	out := make([]string, 0, len(in))
	for _, d := range in {
		if d = strings.ToLower(strings.TrimSpace(d)); d != "" {
			out = append(out, d)
		}
	}
	return out
}

// CheckClaims applies the login policy and returns the lowercased email. An
// empty allowed list refuses everyone. The hd claim is only trusted together
// with the email suffix check, so a token for a different domain cannot pass.
func CheckClaims(c Claims, allowed []string) (string, error) {
	allowed = NormalizeDomains(allowed)
	if len(allowed) == 0 {
		return "", ErrNoDomains
	}
	if !c.EmailVerified {
		return "", ErrEmailUnverified
	}
	// Checked before lowercasing: ToLower maps U+212A to k, so a lookalike would pass.
	if !isASCII(c.Email) {
		return "", ErrEmailNotASCII
	}
	hd := strings.ToLower(strings.TrimSpace(c.HostedDomain))
	if hd == "" {
		return "", ErrMissingDomain
	}
	if !slices.Contains(allowed, hd) {
		return "", ErrDomainNotAllowed
	}
	email := strings.ToLower(strings.TrimSpace(c.Email))
	local, domain, ok := strings.Cut(email, "@")
	if !ok || local == "" || domain != hd {
		return "", ErrEmailMismatch
	}
	return email, nil
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}
