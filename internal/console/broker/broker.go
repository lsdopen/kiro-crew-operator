// Package broker signs an owner in to their crew's dashboard. It execs
// `kirocrew token` in the crew's gateway container and builds the redirect
// target. The token exists only in memory and in the returned URL: no error from
// this package contains it, so callers can log errors freely.
package broker

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/lsdopen/kiro-crew-operator/api/v1alpha1"
)

const (
	// Container is the crew container that runs the gateway.
	Container = "gateway"
	// TokenTTL is the link window requested from the gateway. The chart's
	// ValidatingAdmissionPolicy admits exactly this command.
	TokenTTL = "5m"

	execTimeout = 20 * time.Second
	// Labels the operator puts on a crew's pods (labelsFor in the controller).
	labelName     = "app.kubernetes.io/name"
	labelInstance = "app.kubernetes.io/instance"
	labelNameVal  = "kiro-crew"
)

// Command is the only command the broker execs.
var Command = []string{"kirocrew", "token", "--ttl", TokenTTL}

var (
	// ErrNotReady means the crew has no dashboard address or no Ready pod yet.
	ErrNotReady = errors.New("crew is not ready")
	// ErrRefused means the crew's dashboard address is not an allowed tailnet https URL.
	ErrRefused = errors.New("dashboard address refused")
	// ErrNoToken means the gateway printed no sign-in link for the dashboard host.
	ErrNoToken = errors.New("gateway printed no sign-in link")
)

// Execer runs one command in a pod container and returns its stdout. Its errors
// must never carry the command's output.
type Execer interface {
	Exec(ctx context.Context, namespace, pod, container string, command []string) ([]byte, error)
}

// Broker mints sign-in redirects. All fields are required.
type Broker struct {
	// Pods reads pods live, so no pod informer or watch is needed.
	Pods client.Reader
	Exec Execer
	// TailnetDomain is the MagicDNS suffix a dashboard host must end with.
	TailnetDomain string
}

// Location returns the URL to redirect the crew's owner to. Authorisation is the
// caller's job.
func (b *Broker) Location(ctx context.Context, crew *v1alpha1.KiroCrew) (string, error) {
	if crew.Status.DashboardURL == "" {
		return "", fmt.Errorf("%w: no dashboard address yet", ErrNotReady)
	}
	host, err := DashboardHost(crew.Status.DashboardURL, b.TailnetDomain)
	if err != nil {
		return "", err
	}
	pod, err := b.readyPod(ctx, crew)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, execTimeout)
	defer cancel()
	out, err := b.Exec.Exec(ctx, pod.Namespace, pod.Name, Container, Command)
	if err != nil {
		return "", fmt.Errorf("exec in %s: %w", pod.Name, err)
	}
	token := ParseToken(string(out), host)
	if token == "" {
		return "", fmt.Errorf("%w for %s", ErrNoToken, host)
	}
	return "https://" + host + "/?" + url.Values{"token": {token}}.Encode(), nil
}

// DashboardHost returns the host of raw if it is a plain https URL on a
// subdomain of tailnetDomain, and ErrRefused otherwise.
func DashboardHost(raw, tailnetDomain string) (string, error) {
	suffix := "." + strings.Trim(strings.ToLower(tailnetDomain), ".")
	u, err := url.Parse(raw)
	switch {
	case err != nil, u.Scheme != "https", u.User != nil, u.Port() != "", u.RawQuery != "", u.Fragment != "",
		u.Path != "" && u.Path != "/":
		return "", ErrRefused
	}
	host := strings.ToLower(u.Hostname())
	if len(suffix) < 2 || !strings.HasSuffix(host, suffix) || len(host) == len(suffix) || strings.HasPrefix(host, ".") {
		return "", ErrRefused
	}
	return host, nil
}

// ParseToken returns the token of the first https URL in out whose host is host,
// or "". Other lines (the loopback URL, warnings) are ignored.
func ParseToken(out, host string) string {
	for line := range strings.SplitSeq(out, "\n") {
		u, err := url.Parse(strings.TrimSpace(line))
		if err != nil || u.Scheme != "https" || !strings.EqualFold(u.Host, host) {
			continue
		}
		if t := u.Query().Get("token"); t != "" {
			return t
		}
	}
	return ""
}

// readyPod returns the crew's Running, Ready pod.
func (b *Broker) readyPod(ctx context.Context, crew *v1alpha1.KiroCrew) (*corev1.Pod, error) {
	var pods corev1.PodList
	err := b.Pods.List(ctx, &pods, client.InNamespace(crew.Namespace),
		client.MatchingLabels{labelName: labelNameVal, labelInstance: crew.Name})
	if err != nil {
		return nil, fmt.Errorf("listing pods: %w", err)
	}
	for i := range pods.Items {
		if p := &pods.Items[i]; podReady(p) {
			return p, nil
		}
	}
	return nil, fmt.Errorf("%w: no ready pod", ErrNotReady)
}

func podReady(p *corev1.Pod) bool {
	if p.DeletionTimestamp != nil || p.Status.Phase != corev1.PodRunning {
		return false
	}
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}
