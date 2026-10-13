package broker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

// These must match dist/chart/templates/console/vap.yaml; the test below fails if not.
const (
	vapExpression = "has(object.container) && object.container == 'gateway' && object.command == ['kirocrew', 'token', '--ttl', '5m'] && !(has(object.stdin) && object.stdin) && !(has(object.tty) && object.tty)"
	vapMessage    = "the console may only exec 'kirocrew token --ttl 5m' in the gateway container, without stdin or a tty"
	vapUser       = "system:serviceaccount:kiro-crew:console"
	vapNamespace  = "kiro-crews"
	vapPod        = "alice"
	vapRole       = "console"
	vapPolicy     = "console-exec"
	kirocrewBin   = "kirocrew"
)

func envtestAssets(t *testing.T) string {
	t.Helper()
	if dir := os.Getenv("KUBEBUILDER_ASSETS"); dir != "" {
		return dir
	}
	base, _ := filepath.Abs(filepath.Join("..", "..", "..", "bin", "k8s"))
	if entries, err := os.ReadDir(base); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				return filepath.Join(base, e.Name())
			}
		}
	}
	t.Skip("no envtest binaries: run make test or set KUBEBUILDER_ASSETS")
	return ""
}

func TestChartPolicyMatchesTestedExpression(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "dist", "chart", "templates", "console", "vap.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{vapExpression, vapMessage, "reason: Forbidden", `operations: ["CONNECT"]`, `resources: ["pods/exec"]`, "failurePolicy: Fail"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("vap.yaml lacks %q", want)
		}
	}
	if !strings.Contains(vapExpression, "'"+strings.Join(Command, "', '")+"'") {
		t.Errorf("policy command differs from broker.Command %q", Command)
	}
}

// TestPolicyNarrowsExecToTheTokenCommand runs the chart's policy on a real
// kube-apiserver: a ValidatingAdmissionPolicy sees pods/exec CONNECT and its command.
func TestPolicyNarrowsExecToTheTokenCommand(t *testing.T) {
	env := &envtest.Environment{BinaryAssetsDirectory: envtestAssets(t)}
	cfg, err := env.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = env.Stop() })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	admin := kubernetes.NewForConfigOrDie(cfg)
	setupPolicy(ctx, t, admin)
	console := rest.CopyConfig(cfg)
	console.Impersonate = rest.ImpersonationConfig{UserName: vapUser}
	cs := kubernetes.NewForConfigOrDie(console)

	exec := func(c kubernetes.Interface, opts corev1.PodExecOptions) error {
		return c.CoreV1().RESTClient().Post().Resource("pods").Namespace(vapNamespace).Name(vapPod).SubResource("exec").
			VersionedParams(&opts, scheme.ParameterCodec).Do(ctx).Error()
	}
	denied := func(err error) bool { return apierrors.IsForbidden(err) && strings.Contains(err.Error(), vapMessage) }
	token := corev1.PodExecOptions{Container: Container, Command: Command, Stdout: true}

	// The policy takes a moment to start enforcing.
	var last error
	err = wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 30*time.Second, true, func(context.Context) (bool, error) {
		last = exec(cs, corev1.PodExecOptions{Container: Container, Command: []string{"sh"}, Stdout: true})
		return denied(last), nil
	})
	if err != nil {
		t.Fatalf("policy never denied `sh`; last result: %v", last)
	}

	for name, opts := range map[string]corev1.PodExecOptions{
		"shell":              {Container: Container, Command: []string{"sh"}, Stdout: true},
		"extra argument":     {Container: Container, Command: append(append([]string{}, Command...), "--embed-parent-port", "1"), Stdout: true},
		"longer ttl":         {Container: Container, Command: []string{kirocrewBin, "token", "--ttl", "20h"}, Stdout: true},
		"other command":      {Container: Container, Command: []string{kirocrewBin, "logout"}, Stdout: true},
		"other container":    {Container: "tailscale", Command: Command, Stdout: true},
		"no container named": {Command: Command, Stdout: true},
		"stdin":              {Container: Container, Command: Command, Stdin: true, Stdout: true},
		"tty":                {Container: Container, Command: Command, Stdout: true, TTY: true},
	} {
		if err := exec(cs, opts); !denied(err) {
			t.Errorf("%s: want the policy to deny, got %v", name, err)
		}
	}

	// The token command passes admission. The pod is unscheduled, so the API
	// server then fails to route it, which is not a policy denial.
	if err := exec(cs, token); err == nil || denied(err) || apierrors.IsForbidden(err) {
		t.Errorf("token command: want past admission, got %v", err)
	}
	// Other identities are not constrained by the policy.
	if err := exec(admin, corev1.PodExecOptions{Container: Container, Command: []string{"sh"}, Stdout: true}); err == nil || denied(err) {
		t.Errorf("admin: want past admission, got %v", err)
	}
}

func setupPolicy(ctx context.Context, t *testing.T, cs kubernetes.Interface) {
	t.Helper()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err := cs.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: vapNamespace}}, metav1.CreateOptions{})
	must(err)
	_, err = cs.CoreV1().Pods(vapNamespace).Create(ctx, &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: vapPod},
		Spec: corev1.PodSpec{Containers: []corev1.Container{
			{Name: Container, Image: "example.com/gateway:1.0.0"}, {Name: "tailscale", Image: "example.com/tailscale:1.0.0"},
		}},
	}, metav1.CreateOptions{})
	must(err)
	_, err = cs.RbacV1().Roles(vapNamespace).Create(ctx, &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{Name: vapRole},
		Rules:      []rbacv1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"pods/exec"}, Verbs: []string{"create"}}},
	}, metav1.CreateOptions{})
	must(err)
	_, err = cs.RbacV1().RoleBindings(vapNamespace).Create(ctx, &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: vapRole},
		RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "Role", Name: vapRole},
		Subjects:   []rbacv1.Subject{{APIGroup: "rbac.authorization.k8s.io", Kind: "User", Name: vapUser}},
	}, metav1.CreateOptions{})
	must(err)

	fail, forbidden := admissionv1.Fail, metav1.StatusReasonForbidden
	_, err = cs.AdmissionregistrationV1().ValidatingAdmissionPolicies().Create(ctx, &admissionv1.ValidatingAdmissionPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: vapPolicy},
		Spec: admissionv1.ValidatingAdmissionPolicySpec{
			FailurePolicy: &fail,
			MatchConstraints: &admissionv1.MatchResources{ResourceRules: []admissionv1.NamedRuleWithOperations{{
				RuleWithOperations: admissionv1.RuleWithOperations{
					Operations: []admissionv1.OperationType{admissionv1.Connect},
					Rule:       admissionv1.Rule{APIGroups: []string{""}, APIVersions: []string{"v1"}, Resources: []string{"pods/exec"}},
				},
			}}},
			MatchConditions: []admissionv1.MatchCondition{{
				Name: "console-service-account", Expression: "request.userInfo.username == '" + vapUser + "'",
			}},
			Validations: []admissionv1.Validation{{Expression: vapExpression, Message: vapMessage, Reason: &forbidden}},
		},
	}, metav1.CreateOptions{})
	must(err)
	_, err = cs.AdmissionregistrationV1().ValidatingAdmissionPolicyBindings().Create(ctx, &admissionv1.ValidatingAdmissionPolicyBinding{
		ObjectMeta: metav1.ObjectMeta{Name: vapPolicy},
		Spec: admissionv1.ValidatingAdmissionPolicyBindingSpec{
			PolicyName:        vapPolicy,
			ValidationActions: []admissionv1.ValidationAction{admissionv1.Deny},
			MatchResources: &admissionv1.MatchResources{NamespaceSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"kubernetes.io/metadata.name": vapNamespace},
			}},
		},
	}, metav1.CreateOptions{})
	must(err)
}
