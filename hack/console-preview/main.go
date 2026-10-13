//go:build preview

// Command console-preview runs the console locally against an envtest API server
// and an in-memory OIDC issuer, with sample data. Dev only: the preview build tag
// keeps it out of every image, and cmd/console never imports the fake issuer.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	v1alpha1 "github.com/lsdopen/kiro-crew-operator/api/v1alpha1"
	"github.com/lsdopen/kiro-crew-operator/internal/console/auth"
	"github.com/lsdopen/kiro-crew-operator/internal/console/authz"
	"github.com/lsdopen/kiro-crew-operator/internal/console/oidctest"
	"github.com/lsdopen/kiro-crew-operator/internal/console/web"
)

const (
	crewNamespace = "kiro-crews"
	adminEmail    = "admin@example.com"
	managerEmail  = "manager@example.com"
	aliceEmail    = "alice@example.com"
	bobEmail      = "bob@example.com"
)

func main() {
	port := flag.Int("port", 8080, "Port to serve the console on, 127.0.0.1 only.")
	flag.Parse()
	ctrl.SetLogger(logr.Discard())
	if err := run(*port); err != nil {
		log.Fatal(err)
	}
}

func run(port int) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return err
	}
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		return err
	}

	env := &envtest.Environment{CRDDirectoryPaths: []string{"config/crd/bases"}, ErrorIfCRDPathMissing: true}
	cfg, err := env.Start()
	if err != nil {
		return fmt.Errorf("starting envtest (run via make console-preview so KUBEBUILDER_ASSETS is set): %w", err)
	}
	defer func() { _ = env.Stop() }()

	direct, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		return err
	}
	if err := seed(ctx, direct); err != nil {
		return fmt.Errorf("seeding: %w", err)
	}

	cl, err := cluster.New(cfg, func(o *cluster.Options) {
		o.Scheme = scheme
		ns := map[string]cache.Config{crewNamespace: {}}
		o.Cache = cache.Options{ByObject: map[client.Object]cache.ByObject{
			&v1alpha1.KiroCrew{}:        {Namespaces: ns},
			&v1alpha1.KiroCrewRequest{}: {Namespaces: ns},
		}}
	})
	if err != nil {
		return err
	}
	go func() { _ = cl.Start(ctx) }()
	if !cl.GetCache().WaitForCacheSync(ctx) {
		return errors.New("cache did not sync")
	}

	iss, err := oidctest.New()
	if err != nil {
		return err
	}
	defer iss.Close()
	iss.Handle("GET /authorize", picker(iss))

	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	provider, err := auth.NewOIDC(ctx, auth.OIDCConfig{
		IssuerURL: iss.URL, RedirectURL: base + "/auth/callback",
		ClientID: oidctest.ClientID, ClientSecret: "preview",
	})
	if err != nil {
		return err
	}
	sealer, err := auth.NewSealer([]byte("preview-only-cookie-key-32-bytes"))
	if err != nil {
		return err
	}
	srv := &web.Server{
		Provider: provider, Sealer: sealer, AllowedDomains: []string{"example.com"}, Log: logr.Discard(),
		Store: &authz.Store{Client: cl.GetClient(), Live: cl.GetAPIReader(), CrewNamespace: crewNamespace},
	}

	// Loopback only: the fake issuer will sign in anyone who can reach this port.
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return err
	}
	httpSrv := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		_ = httpSrv.Close()
	}()
	fmt.Printf("\nConsole preview: %s\nPress Ctrl-C to stop.\n\n", base)
	if err := httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

var seededEmails = []string{adminEmail, managerEmail, aliceEmail, bobEmail}

var pickerPage = template.Must(template.New("p").Parse(`<!doctype html>
<meta charset="utf-8"><title>Preview sign-in</title>
<body style="font-family:sans-serif;max-width:28rem;margin:4rem auto">
<h1>Preview sign-in</h1><p>Fake identity provider. Pick who to sign in as.</p>
<ul>{{range .}}<li style="margin:.5rem 0"><a href="{{.URL}}">{{.Email}}</a></li>{{end}}</ul>`))

// picker is the fake provider's authorize page: choose a user, get a code, go back.
func picker(iss *oidctest.Issuer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if email := q.Get("as"); email != "" {
			q.Del("as")
			code, err := iss.Authorize(iss.URL+"/authorize?"+q.Encode(), map[string]any{"email": email})
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			http.Redirect(w, r, q.Get("redirect_uri")+"?code="+code+"&state="+q.Get("state"), http.StatusFound)
			return
		}
		type link struct{ Email, URL string }
		var links []link
		for _, e := range seededEmails {
			q.Set("as", e)
			links = append(links, link{e, "/authorize?" + q.Encode()})
		}
		_ = pickerPage.Execute(w, links)
	}
}

func seed(ctx context.Context, c client.Client) error {
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: crewNamespace}}); err != nil {
		return err
	}
	accounts := []v1alpha1.KiroCrewAccountSpec{
		{Email: adminEmail, Role: v1alpha1.RoleAdmin, DisplayName: "Ada Admin"},
		{Email: managerEmail, Role: v1alpha1.RoleManager, DisplayName: "Max Manager"},
		{Email: aliceEmail, Role: v1alpha1.RoleUser, Manager: managerEmail, DisplayName: "Alice"},
		{Email: bobEmail, Role: v1alpha1.RoleUser, DisplayName: "Bob"},
	}
	for _, s := range accounts {
		a := &v1alpha1.KiroCrewAccount{ObjectMeta: metav1.ObjectMeta{Name: authz.AccountName(s.Email)}, Spec: s}
		if err := c.Create(ctx, a); err != nil {
			return err
		}
	}

	cfg := &v1alpha1.KiroCrewConfig{ObjectMeta: metav1.ObjectMeta{Name: v1alpha1.ConfigName}}
	if err := c.Create(ctx, cfg); err != nil {
		return err
	}

	now := metav1.Now()
	next := metav1.NewTime(now.Add(10 * time.Hour))
	weekdays := &v1alpha1.ScheduleSpec{Up: "0 8 * * 1-5", Down: "0 18 * * 1-5", Timezone: "Africa/Johannesburg"}
	crews := []struct {
		crew   v1alpha1.KiroCrew
		status v1alpha1.KiroCrewStatus
	}{
		{
			v1alpha1.KiroCrew{
				ObjectMeta: metav1.ObjectMeta{Name: "kiro-crew-alice", Namespace: crewNamespace},
				Spec:       v1alpha1.KiroCrewSpec{Owner: aliceEmail, Size: v1alpha1.SizeDevelopment},
			},
			v1alpha1.KiroCrewStatus{
				DashboardURL: "https://kiro-crew-alice.example-tailnet.ts.net",
				Conditions: []metav1.Condition{{
					Type: v1alpha1.ConditionReady, Status: metav1.ConditionTrue, Reason: "Ready",
					LastTransitionTime: now,
				}},
			},
		},
		{
			v1alpha1.KiroCrew{
				ObjectMeta: metav1.ObjectMeta{Name: "kiro-crew-bob", Namespace: crewNamespace},
				Spec:       v1alpha1.KiroCrewSpec{Owner: bobEmail, Size: v1alpha1.SizeMini, Schedule: weekdays},
			},
			v1alpha1.KiroCrewStatus{
				NextTransition: &next,
				Conditions: []metav1.Condition{{
					Type: v1alpha1.ConditionReady, Status: metav1.ConditionFalse,
					Reason: v1alpha1.ReasonOutsideSchedule, LastTransitionTime: now,
				}},
			},
		},
		{
			v1alpha1.KiroCrew{
				ObjectMeta: metav1.ObjectMeta{Name: "kiro-crew-manager", Namespace: crewNamespace},
				Spec:       v1alpha1.KiroCrewSpec{Owner: managerEmail, Size: v1alpha1.SizePower, Suspend: true},
			},
			v1alpha1.KiroCrewStatus{Suspended: true},
		},
	}
	for i := range crews {
		cr := &crews[i].crew
		if err := c.Create(ctx, cr); err != nil {
			return err
		}
		cr.Status = crews[i].status
		if err := c.Status().Update(ctx, cr); err != nil {
			return err
		}
	}

	decided := metav1.Now()
	reqs := []struct {
		req    v1alpha1.KiroCrewRequest
		status v1alpha1.KiroCrewRequestStatus
	}{
		{
			v1alpha1.KiroCrewRequest{
				ObjectMeta: metav1.ObjectMeta{Name: "req-alice-1", Namespace: crewNamespace},
				Spec: v1alpha1.KiroCrewRequestSpec{
					Requester: aliceEmail, Size: v1alpha1.SizeLight, Reason: "Second crew for the data migration",
				},
			},
			v1alpha1.KiroCrewRequestStatus{Phase: v1alpha1.PhasePending},
		},
		{
			v1alpha1.KiroCrewRequest{
				ObjectMeta: metav1.ObjectMeta{Name: "req-alice-0", Namespace: crewNamespace},
				Spec: v1alpha1.KiroCrewRequestSpec{
					Requester: aliceEmail, Size: v1alpha1.SizeDevelopment, Reason: "Daily development crew",
					Decision: &v1alpha1.Decision{Verdict: v1alpha1.VerdictApproved, By: managerEmail},
				},
			},
			v1alpha1.KiroCrewRequestStatus{
				Phase: v1alpha1.PhaseProvisioned, DecidedBy: managerEmail, DecidedAt: &decided,
				CrewRef: "kiro-crew-alice",
			},
		},
		{
			v1alpha1.KiroCrewRequest{
				ObjectMeta: metav1.ObjectMeta{Name: "req-bob-1", Namespace: crewNamespace},
				Spec: v1alpha1.KiroCrewRequestSpec{
					Requester: bobEmail, Size: v1alpha1.SizePower, Reason: "Trying a power crew for fun",
					Decision: &v1alpha1.Decision{Verdict: v1alpha1.VerdictDenied, By: adminEmail},
				},
			},
			v1alpha1.KiroCrewRequestStatus{
				Phase: v1alpha1.PhaseDenied, DecidedBy: adminEmail, DecidedAt: &decided,
			},
		},
	}
	for i := range reqs {
		r := &reqs[i].req
		if err := c.Create(ctx, r); err != nil {
			return err
		}
		r.Status = reqs[i].status
		if err := c.Status().Update(ctx, r); err != nil {
			return err
		}
	}
	return nil
}
