package web

import (
	"fmt"
	"net/url"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/lsdopen/kiro-crew-operator/api/v1alpha1"
	"github.com/lsdopen/kiro-crew-operator/internal/console/authz"
)

// Page is everything a template renders.
type Page struct {
	Email string
	Role  string
	CSRF  string
	// Active is the section shown: crews, requests or accounts.
	Active   string
	Crews    []CrewRow
	Requests []RequestRow
	Accounts []AccountRow
	// ShowOwner adds the owner column, for views that span several people.
	ShowOwner bool
	// Notice is a one-line result of the last action.
	Notice string
	// Sizes are the choices on the request form.
	Sizes []string
	// CanOpen shows the Open button, which needs sign-in brokering.
	CanOpen bool
	// NoManager warns that only an admin can approve this person's requests.
	NoManager bool
}

// CrewRow is one crew card row.
type CrewRow struct {
	Name     string
	Owner    string
	Phase    string
	Schedule string
	// DashboardURL and LoginURL are set for the owner only.
	DashboardURL string
	LoginURL     string
	// CanControl gates the Start and Stop buttons; the server checks again on post.
	CanControl bool
}

// RequestRow is one request.
type RequestRow struct {
	Name      string
	Requester string
	Size      string
	Phase     string
	DecidedBy string
	CrewRef   string
	Reason    string
	// CanDecide and CanCancel gate the row's buttons; the server checks again on post.
	CanDecide bool
	CanCancel bool
}

// AccountRow is one account, shown to admins.
type AccountRow struct {
	Email       string
	DisplayName string
	Role        string
	Manager     string
}

func newPage(v *authz.View, csrf, active string) Page {
	me := v.Me.Spec.Email
	p := Page{
		Email: me, Role: string(v.Me.Spec.Role), CSRF: csrf, Active: active,
		ShowOwner: v.Me.Spec.Role != v1alpha1.RoleUser,
		Sizes:     sizeNames(), NoManager: v.Me.Spec.Manager == "",
	}
	for i := range v.Crews {
		row := crewRow(&v.Crews[i], me)
		row.CanControl = v.Controllable[row.Name]
		p.Crews = append(p.Crews, row)
	}
	for i := range v.Requests {
		q := &v.Requests[i]
		row := requestRow(q)
		pending := authz.Pending(q)
		row.CanDecide = pending && v.Decidable[q.Name]
		row.CanCancel = pending && strings.EqualFold(q.Spec.Requester, me)
		p.Requests = append(p.Requests, row)
	}
	for i := range v.Accounts {
		a := &v.Accounts[i].Spec
		p.Accounts = append(p.Accounts, AccountRow{
			Email: a.Email, DisplayName: a.DisplayName, Role: string(a.Role), Manager: a.Manager,
		})
	}
	slices.SortFunc(p.Crews, func(a, b CrewRow) int { return strings.Compare(a.Name, b.Name) })
	slices.SortFunc(p.Requests, func(a, b RequestRow) int { return strings.Compare(a.Name, b.Name) })
	slices.SortFunc(p.Accounts, func(a, b AccountRow) int { return strings.Compare(a.Email, b.Email) })
	return p
}

// crewRow hides the dashboard and node-auth links from everyone but the owner:
// the second is a one-time credential, and neither is usable by anyone else.
func crewRow(c *v1alpha1.KiroCrew, me string) CrewRow {
	r := CrewRow{
		Name: c.Name, Owner: c.Spec.Owner, Phase: crewPhase(c), Schedule: scheduleText(c),
	}
	if strings.EqualFold(c.Spec.Owner, me) {
		r.DashboardURL = httpsOnly(c.Status.DashboardURL)
		r.LoginURL = httpsOnly(c.Status.TailnetLoginURL)
	}
	return r
}

func requestRow(q *v1alpha1.KiroCrewRequest) RequestRow {
	phase := string(q.Status.Phase)
	if phase == "" {
		phase = string(v1alpha1.PhasePending)
	}
	return RequestRow{
		Name: q.Name, Requester: q.Spec.Requester, Size: string(q.Spec.Size), Phase: phase,
		DecidedBy: q.Status.DecidedBy, CrewRef: q.Status.CrewRef, Reason: q.Spec.Reason,
	}
}

// sizeNames lists the tiers smallest first.
func sizeNames() []string {
	return []string{
		string(v1alpha1.SizeMini), string(v1alpha1.SizeLight),
		string(v1alpha1.SizeDevelopment), string(v1alpha1.SizePower),
	}
}

func crewPhase(c *v1alpha1.KiroCrew) string {
	ready := meta.FindStatusCondition(c.Status.Conditions, v1alpha1.ConditionReady)
	switch {
	case ready == nil && c.Status.Suspended:
		return v1alpha1.ReasonSuspended
	case ready == nil:
		return "Pending"
	case ready.Status == metav1.ConditionTrue:
		return "Ready"
	case ready.Reason != "":
		return ready.Reason
	default:
		return "NotReady"
	}
}

func scheduleText(c *v1alpha1.KiroCrew) string {
	s := c.Spec.Schedule
	if s == nil {
		return "always on"
	}
	text := fmt.Sprintf("up %s, down %s", s.Up, s.Down)
	if s.Timezone != "" {
		text += " (" + s.Timezone + ")"
	}
	if t := c.Status.NextTransition; t != nil {
		text += ", next " + t.UTC().Format("2006-01-02 15:04 MST")
	}
	return text
}

// httpsOnly drops anything that is not an https URL, so a hostile status value
// can never become a javascript: or http: link.
func httpsOnly(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return ""
	}
	return u.String()
}
