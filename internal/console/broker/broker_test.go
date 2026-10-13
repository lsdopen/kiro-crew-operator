package broker

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/lsdopen/kiro-crew-operator/api/v1alpha1"
)

const (
	domain   = "example-tailnet.ts.net"
	dash     = "https://alice." + domain
	aliceNS  = "kiro-crews"
	podName  = "kiro-crew-alice-0"
	testJWT  = "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJhbGljZSJ9.c2lnbmF0dXJl"
	loopback = "http://localhost:8080?token=loopback-token"
)

type fakeExec struct {
	out   string
	err   error
	calls [][]string
}

func (f *fakeExec) Exec(_ context.Context, namespace, pod, container string, command []string) ([]byte, error) {
	f.calls = append(f.calls, append([]string{namespace, pod, container}, command...))
	return []byte(f.out), f.err
}

func crew(dashboardURL string) *v1alpha1.KiroCrew {
	return &v1alpha1.KiroCrew{
		ObjectMeta: metav1.ObjectMeta{Name: "alice", Namespace: aliceNS},
		Spec:       v1alpha1.KiroCrewSpec{Owner: "alice@example.com"},
		Status:     v1alpha1.KiroCrewStatus{DashboardURL: dashboardURL},
	}
}

func pod(name string, ready bool) *corev1.Pod {
	status := corev1.ConditionFalse
	if ready {
		status = corev1.ConditionTrue
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: aliceNS, Labels: map[string]string{
			"app.kubernetes.io/name": "kiro-crew", "app.kubernetes.io/instance": "alice",
		}},
		Status: corev1.PodStatus{
			Phase:      corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: status}},
		},
	}
}

func newBroker(t *testing.T, ex Execer, objs ...client.Object) *Broker {
	t.Helper()
	s := runtime.NewScheme()
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return &Broker{
		Pods: fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).Build(), Exec: ex, TailnetDomain: domain,
	}
}

func TestDashboardHost(t *testing.T) {
	tests := []struct {
		name, raw, domain string
		want              string
	}{
		{"valid", dash, domain, "alice." + domain},
		{"trailing slash", dash + "/", domain, "alice." + domain},
		{"host case", "https://ALICE." + domain, domain, "alice." + domain},
		{"domain with dots and case", dash, "." + strings.ToUpper(domain) + ".", "alice." + domain},
		{"nested subdomain", "https://a.b." + domain, domain, "a.b." + domain},
		{"http", "http://alice." + domain, domain, ""},
		{"no scheme", "alice." + domain, domain, ""},
		{"javascript", "javascript:alert(1)", domain, ""},
		{"other domain", "https://alice.example.com", domain, ""},
		{"suffix without the dot", "https://evil" + domain, domain, ""},
		{"domain as a prefix", "https://alice." + domain + ".evil.example.com", domain, ""},
		{"the domain itself", "https://" + domain, domain, ""},
		{"non-ASCII host byte", "https://alice\u212a." + domain, domain, ""},
		{"non-ASCII lookalike of the domain", "https://alice." + strings.Replace(domain, "s", "\u017f", 1), domain, ""},
		{"empty label", "https://." + domain, domain, ""},
		{"userinfo host confusion", "https://" + domain + "@evil.example.com", domain, ""},
		{"userinfo", "https://user@alice." + domain, domain, ""},
		{"port", dash + ":8443", domain, ""},
		{"path", dash + "/app", domain, ""},
		{"query", dash + "/?x=1", domain, ""},
		{"fragment", dash + "/#x", domain, ""},
		{"empty", "", domain, ""},
		{"unconfigured domain", dash, "", ""},
		{"dot-only domain", dash, ".", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := DashboardHost(tt.raw, tt.domain)
			if tt.want == "" {
				if !errors.Is(err, ErrRefused) {
					t.Fatalf("got %q, %v; want ErrRefused", got, err)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("got %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestParseToken(t *testing.T) {
	host := "alice." + domain
	tests := []struct {
		name, out, want string
	}{
		{"only the tailnet url", dash + "/?token=" + testJWT + "\n", testJWT},
		{"after loopback and origin lines", loopback + "\n\nhttps://other.example.com/?token=x\n" + dash + "/?token=" + testJWT + "\n", testJWT},
		{"crlf", dash + "/?token=" + testJWT + "\r\n", testJWT},
		{"host case", "https://ALICE." + domain + "/?token=" + testJWT, testJWT},
		{"loopback only", loopback + "\n", ""},
		{"other host only", "https://bob." + domain + "/?token=x\n", ""},
		{"http on the right host", "http://" + host + "/?token=x\n", ""},
		{"empty token", dash + "/?token=\n", ""},
		{"no token", dash + "/\n", ""},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ParseToken(tt.out, host); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLocationIsExactAndExecsOnlyTheTokenCommand(t *testing.T) {
	ex := &fakeExec{out: loopback + "\n\n" + dash + "/?token=" + testJWT + "\n"}
	b := newBroker(t, ex, pod(podName, true))
	got, err := b.Location(context.Background(), crew(dash))
	if err != nil {
		t.Fatal(err)
	}
	if want := dash + "/?token=" + testJWT; got != want {
		t.Fatalf("location = %q, want %q", got, want)
	}
	want := append([]string{aliceNS, podName, Container}, Command...)
	if len(ex.calls) != 1 || !slices.Equal(ex.calls[0], want) {
		t.Fatalf("exec calls = %q, want %q", ex.calls, want)
	}
}

func TestLocationEscapesTheToken(t *testing.T) {
	ex := &fakeExec{out: dash + "/?token=a%26b%0d%0aSet-Cookie%3Ax\n"}
	b := newBroker(t, ex, pod(podName, true))
	got, err := b.Location(context.Background(), crew(dash))
	if err != nil || got != dash+"/?token=a%26b%0D%0ASet-Cookie%3Ax" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestLocationRefusals(t *testing.T) {
	tests := []struct {
		name string
		c    *v1alpha1.KiroCrew
		out  string
		objs []client.Object
		want error
	}{
		{"http url", crew("http://alice." + domain), dash, []client.Object{pod(podName, true)}, ErrRefused},
		{"wrong host", crew("https://alice.evil.example.com"), dash, []client.Object{pod(podName, true)}, ErrRefused},
		{"no dashboard yet", crew(""), dash, []client.Object{pod(podName, true)}, ErrNotReady},
		{"no pod", crew(dash), dash, nil, ErrNotReady},
		{"pod not ready", crew(dash), dash, []client.Object{pod(podName, false)}, ErrNotReady},
		{"gateway prints no link for the host", crew(dash), loopback + "\nhttps://bob." + domain + "/?token=x\n",
			[]client.Object{pod(podName, true)}, ErrNoToken},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ex := &fakeExec{out: tt.out}
			b := newBroker(t, ex, tt.objs...)
			got, err := b.Location(context.Background(), tt.c)
			if !errors.Is(err, tt.want) || got != "" {
				t.Fatalf("got %q, %v; want %v", got, err, tt.want)
			}
			if tt.want != ErrNoToken && len(ex.calls) != 0 {
				t.Fatalf("exec ran: %q", ex.calls)
			}
		})
	}
}

func TestLocationIgnoresAnotherCrewsPod(t *testing.T) {
	other := pod("kiro-crew-bob-0", true)
	other.Labels["app.kubernetes.io/instance"] = "bob"
	ex := &fakeExec{out: dash + "/?token=" + testJWT}
	b := newBroker(t, ex, other)
	if _, err := b.Location(context.Background(), crew(dash)); !errors.Is(err, ErrNotReady) || len(ex.calls) != 0 {
		t.Fatalf("err = %v, calls = %q", err, ex.calls)
	}
}

func TestExecErrorsAndOutputNeverReachTheError(t *testing.T) {
	ex := &fakeExec{out: dash + "/?token=" + testJWT, err: errors.New("command exited 1")}
	b := newBroker(t, ex, pod(podName, true))
	_, err := b.Location(context.Background(), crew(dash))
	if err == nil || strings.Contains(err.Error(), testJWT) {
		t.Fatalf("err = %v", err)
	}
	ex = &fakeExec{out: "https://bob." + domain + "/?token=" + testJWT}
	_, err = newBroker(t, ex, pod(podName, true)).Location(context.Background(), crew(dash))
	if !errors.Is(err, ErrNoToken) || strings.Contains(err.Error(), testJWT) {
		t.Fatalf("err = %v", err)
	}
}
