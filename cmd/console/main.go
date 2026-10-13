// Command console is the Kiro Crew management console. It is a separate binary
// from the manager and reads KiroCrew resources with its own ServiceAccount.
package main

import (
	"context"
	"errors"
	"flag"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	kirocrewv1alpha1 "github.com/lsdopen/kiro-crew-operator/api/v1alpha1"
	"github.com/lsdopen/kiro-crew-operator/internal/console/auth"
	"github.com/lsdopen/kiro-crew-operator/internal/console/authz"
	"github.com/lsdopen/kiro-crew-operator/internal/console/web"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(kirocrewv1alpha1.AddToScheme(scheme))
}

type config struct {
	addr, provider, issuerURL, redirectURL, secretDir, crewNamespace string
	allowedDomains                                                   []string
	allowEmptyDomains                                                bool
}

func parseFlags() config {
	var c config
	var domains string
	flag.StringVar(&c.addr, "bind-address", ":8080", "Address the HTTP server listens on.")
	flag.StringVar(&c.provider, "auth-provider", "google", "OIDC provider. Only google is implemented.")
	flag.StringVar(&c.issuerURL, "auth-issuer-url", "https://accounts.google.com", "OIDC issuer URL.")
	flag.StringVar(&c.redirectURL, "auth-redirect-url", "", "OAuth redirect URL, https://<host>/auth/callback.")
	flag.StringVar(&domains, "auth-allowed-domains", "", "Comma-separated Workspace domains allowed to sign in.")
	flag.BoolVar(&c.allowEmptyDomains, "auth-allow-empty-domains", false,
		"Start with no allowed domains (every login is refused). For testing.")
	flag.StringVar(&c.secretDir, "auth-secret-dir", "/etc/console/auth",
		"Directory holding the client-id, client-secret and cookie-key files.")
	flag.StringVar(&c.crewNamespace, "crew-namespace", "", "Namespace holding KiroCrews and requests. Empty means all.")
	opts := zap.Options{Development: true}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	c.allowedDomains = auth.NormalizeDomains(strings.Split(domains, ","))
	return c
}

func (c config) validate() error {
	if c.provider != "google" {
		return errors.New("only --auth-provider=google is implemented")
	}
	if err := auth.ValidateDomains(c.allowedDomains, c.allowEmptyDomains); err != nil {
		return err
	}
	u, err := url.Parse(c.redirectURL)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return errors.New("--auth-redirect-url must be an https URL")
	}
	return nil
}

func fatal(err error, msg string) {
	setupLog.Error(err, msg)
	os.Exit(1)
}

func main() {
	cfg := parseFlags()
	if err := cfg.validate(); err != nil {
		fatal(err, "Invalid configuration")
	}
	secrets, err := auth.LoadSecrets(cfg.secretDir)
	if err != nil {
		fatal(err, "Could not read auth secrets")
	}
	sealer, err := auth.NewSealer(secrets.CookieKey)
	if err != nil {
		fatal(err, "Invalid cookie key")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cl, err := newCluster(cfg.crewNamespace)
	if err != nil {
		fatal(err, "Could not build the Kubernetes client")
	}
	go func() {
		if err := cl.Start(ctx); err != nil {
			fatal(err, "Cache stopped")
		}
	}()
	// Start the informers now so missing RBAC or CRDs fail at boot, not on first use.
	for _, obj := range []client.Object{
		&kirocrewv1alpha1.KiroCrew{}, &kirocrewv1alpha1.KiroCrewRequest{}, &kirocrewv1alpha1.KiroCrewAccount{},
	} {
		if _, err := cl.GetCache().GetInformer(ctx, obj); err != nil {
			fatal(err, "Could not start informer")
		}
	}

	discoverCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	provider, err := auth.NewOIDC(discoverCtx, auth.OIDCConfig{
		IssuerURL: cfg.issuerURL, RedirectURL: cfg.redirectURL,
		ClientID: secrets.ClientID, ClientSecret: secrets.ClientSecret,
	})
	cancel()
	if err != nil {
		fatal(err, "OIDC discovery failed")
	}

	srv := &web.Server{
		Provider: provider,
		Sealer:   sealer,
		Store: &authz.Store{
			Client: cl.GetClient(), Live: cl.GetAPIReader(), CrewNamespace: cfg.crewNamespace,
		},
		AllowedDomains: cfg.allowedDomains,
		Log:            ctrl.Log.WithName("console"),
	}
	httpSrv := &http.Server{
		Addr: cfg.addr, Handler: srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second,
		WriteTimeout: 30 * time.Second, IdleTimeout: 2 * time.Minute,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, c := context.WithTimeout(context.Background(), 10*time.Second)
		defer c()
		_ = httpSrv.Shutdown(shutdownCtx)
	}()

	setupLog.Info("Starting console", "addr", cfg.addr, "allowedDomains", len(cfg.allowedDomains))
	if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fatal(err, "HTTP server failed")
	}
}

// newCluster builds a cached read client. Crews and requests are limited to the
// crew namespace; accounts are cluster-scoped.
func newCluster(crewNamespace string) (cluster.Cluster, error) {
	byObject := map[client.Object]cache.ByObject{}
	if crewNamespace != "" {
		ns := map[string]cache.Config{crewNamespace: {}}
		byObject[&kirocrewv1alpha1.KiroCrew{}] = cache.ByObject{Namespaces: ns}
		byObject[&kirocrewv1alpha1.KiroCrewRequest{}] = cache.ByObject{Namespaces: ns}
	}
	return cluster.New(ctrl.GetConfigOrDie(), func(o *cluster.Options) {
		o.Scheme = scheme
		o.Cache = cache.Options{ByObject: byObject}
	})
}
