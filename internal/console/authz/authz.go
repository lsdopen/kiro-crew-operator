// Package authz resolves a session email to an account and decides what that
// account may see. The console uses its own ServiceAccount, so these checks are
// the enforcement point.
package authz

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/lsdopen/kiro-crew-operator/api/v1alpha1"
)

// Store reads from the informer cache and falls back to the live API on an
// account cache miss, to settle a sign-up race.
type Store struct {
	// Client reads (cached) and creates accounts.
	Client client.Client
	// Live reads straight from the API server.
	Live client.Reader
	// CrewNamespace holds KiroCrews and KiroCrewRequests.
	CrewNamespace string
}

// FindAccount returns the account for email, matched case-insensitively like the
// operator's findAccount, or nil.
func FindAccount(accounts []v1alpha1.KiroCrewAccount, email string) *v1alpha1.KiroCrewAccount {
	for i := range accounts {
		if strings.EqualFold(accounts[i].Spec.Email, email) {
			return &accounts[i]
		}
	}
	return nil
}

// Find returns the account for email, or nil if there is none. Unlike Resolve it
// never creates one, so a deleted account cannot be recreated by a stale session.
func (s *Store) Find(ctx context.Context, email string) (*v1alpha1.KiroCrewAccount, error) {
	var list v1alpha1.KiroCrewAccountList
	if err := s.Client.List(ctx, &list); err != nil {
		return nil, fmt.Errorf("listing accounts: %w", err)
	}
	if a := FindAccount(list.Items, email); a != nil {
		return a.DeepCopy(), nil
	}
	// The cache may not have seen a just-created account yet; ask the API server
	// so a first login does not need a second Google round-trip.
	got := &v1alpha1.KiroCrewAccount{}
	err := s.Live.Get(ctx, client.ObjectKey{Name: AccountName(email)}, got)
	if apierrors.IsNotFound(err) {
		return nil, nil //nolint:nilnil // nil means "no such account"
	}
	if err != nil {
		return nil, fmt.Errorf("reading account live: %w", err)
	}
	return got, nil
}

// Resolve returns the account for email, creating a role=user, no-manager one on
// first login. The role and manager are fixed here and never come from input.
func (s *Store) Resolve(ctx context.Context, email string) (*v1alpha1.KiroCrewAccount, error) {
	if a, err := s.Find(ctx, email); err != nil || a != nil {
		return a, err
	}
	acct := &v1alpha1.KiroCrewAccount{
		ObjectMeta: metav1.ObjectMeta{Name: AccountName(email)},
		Spec:       v1alpha1.KiroCrewAccountSpec{Email: email, Role: v1alpha1.RoleUser},
	}
	err := s.Client.Create(ctx, acct)
	if apierrors.IsAlreadyExists(err) {
		// The name is derived from the email, so a concurrent first login won.
		// Read it live: the cache may not have seen it yet.
		got := &v1alpha1.KiroCrewAccount{}
		if gerr := s.Live.Get(ctx, client.ObjectKey{Name: acct.Name}, got); gerr != nil {
			return nil, fmt.Errorf("reading account after conflict: %w", gerr)
		}
		return got, nil
	}
	if err != nil {
		return nil, fmt.Errorf("creating account: %w", err)
	}
	return acct, nil
}

var nonDNS = regexp.MustCompile(`[^a-z0-9]+`)

// AccountName is a deterministic DNS-1123 name for email: a readable local part
// plus a hash of the whole lowercased address, so two logins race on one name.
func AccountName(email string) string {
	email = strings.ToLower(strings.TrimSpace(email))
	local, _, _ := strings.Cut(email, "@")
	local = strings.Trim(nonDNS.ReplaceAllString(local, "-"), "-")
	if len(local) > 40 {
		local = strings.Trim(local[:40], "-")
	}
	sum := sha256.Sum256([]byte(email))
	suffix := hex.EncodeToString(sum[:])[:10]
	if local == "" {
		return suffix
	}
	return local + "-" + suffix
}

// View is what one account may see.
type View struct {
	Me       *v1alpha1.KiroCrewAccount
	Crews    []v1alpha1.KiroCrew
	Requests []v1alpha1.KiroCrewRequest
	// Accounts is set for admins only.
	Accounts []v1alpha1.KiroCrewAccount
	// Decidable names the requests Me may approve or deny, by CanDecide.
	Decidable map[string]bool
}

// Visible lists what me may see: users their own, managers also their reports',
// admins everything.
func (s *Store) Visible(ctx context.Context, me *v1alpha1.KiroCrewAccount) (*View, error) {
	var accounts v1alpha1.KiroCrewAccountList
	var crews v1alpha1.KiroCrewList
	var reqs v1alpha1.KiroCrewRequestList
	ns := client.InNamespace(s.CrewNamespace)
	if err := s.Client.List(ctx, &accounts); err != nil {
		return nil, fmt.Errorf("listing accounts: %w", err)
	}
	if err := s.Client.List(ctx, &crews, ns); err != nil {
		return nil, fmt.Errorf("listing crews: %w", err)
	}
	if err := s.Client.List(ctx, &reqs, ns); err != nil {
		return nil, fmt.Errorf("listing requests: %w", err)
	}

	v := &View{Me: me}
	if me.Spec.Role == v1alpha1.RoleAdmin {
		v.Crews, v.Requests, v.Accounts = crews.Items, reqs.Items, accounts.Items
		return v.markDecidable(accounts.Items), nil
	}
	people := []string{me.Spec.Email}
	if me.Spec.Role == v1alpha1.RoleManager {
		for i := range accounts.Items {
			if m := accounts.Items[i].Spec.Manager; m != "" && strings.EqualFold(m, me.Spec.Email) {
				people = append(people, accounts.Items[i].Spec.Email)
			}
		}
	}
	for i := range crews.Items {
		if isOneOf(people, crews.Items[i].Spec.Owner) {
			v.Crews = append(v.Crews, crews.Items[i])
		}
	}
	for i := range reqs.Items {
		if isOneOf(people, reqs.Items[i].Spec.Requester) {
			v.Requests = append(v.Requests, reqs.Items[i])
		}
	}
	return v.markDecidable(accounts.Items), nil
}

func (v *View) markDecidable(accounts []v1alpha1.KiroCrewAccount) *View {
	v.Decidable = map[string]bool{}
	for i := range v.Requests {
		q := &v.Requests[i]
		if CanDecide(v.Me, FindAccount(accounts, q.Spec.Requester)) {
			v.Decidable[q.Name] = true
		}
	}
	return v
}

func isOneOf(people []string, email string) bool {
	for _, p := range people {
		if strings.EqualFold(p, email) {
			return true
		}
	}
	return false
}
