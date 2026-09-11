package controller

import (
	"context"
	"fmt"
	"regexp"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kirocrewv1alpha1 "github.com/lsdopen/kiro-crew-operator/api/v1alpha1"
)

const (
	// gatewayPort is where the Kiro Crew dashboard listens. It is bound to
	// loopback only, so nothing on the pod network can reach it: the sole path in
	// is the tailscale sidecar in the same pod, which is what makes the tailnet's
	// access decision the only one that matters.
	gatewayPort = 8080

	// dataMountPath is KIROCREW_HOME — SQLite databases, the encrypted Kiro token
	// vault, and the workspace all live here, so it must be persistent.
	dataMountPath = "/data"

	// tailscaleStatePath holds the tailnet node identity. It is persisted so a
	// pod restart rejoins as the same user-owned node instead of demanding that
	// the owner authenticate interactively again.
	tailscaleStatePath = "/var/lib/tailscale"

	mcpConfigMountPath = "/etc/kiro-crew"

	// configMountPath is where the gateway's config.json is mounted. It is a
	// distinct directory from KIROCREW_HOME so a read-only ConfigMap mount does
	// not collide with the writable data volume.
	configMountPath = "/etc/kiro-crew-config"

	// defaultGatewayImage is our derived image (see images/gateway/Dockerfile):
	// upstream's own gateway plus uv/uvx, node/npm/npx and the tailscale CLI.
	// Upstream's stock image carries kiro-cli and the gateway but none of those
	// runtimes, and the MCP ecosystem is distributed almost entirely as uvx and
	// npx commands — so on the stock image a crew's MCP servers cannot start.
	// Override spec.gateway.image to run upstream's image directly instead.
	defaultGatewayImage   = "ghcr.io/lsdopen/kiro-crew-gateway:latest"
	defaultTailscaleImage = "ghcr.io/tailscale/tailscale:stable"

	// gatewayHealthPath is upstream's health endpoint, used by its own
	// HEALTHCHECK. It is not /healthz.
	gatewayHealthPath = "/api/health"

	// The upstream image creates and runs as uid/gid 1000.
	gatewayUIDValue int64 = 1000

	// namePrefix keeps every generated object and tailnet hostname under one
	// recognisable prefix.
	namePrefix = "kiro-crew"

	// dataVolumeName is the single persistent volume every instance owns: the
	// gateway's KIROCREW_HOME and the tailnet node identity both live on it.
	dataVolumeName = "data"

	// tailscaleContainerName is the sidecar's name, and also the subPath its
	// state occupies on the data volume.
	tailscaleContainerName = "tailscale"

	// tailnetPollInterval is how often to re-check a node that has not yet been
	// authenticated by its owner.
	tailnetPollInterval = 30 * time.Second
)

// tailnetLoginURLPattern matches the interactive login URL the Tailscale client
// prints while waiting to be authenticated. The operator lifts it into status
// because it cannot be delivered over the tailnet the node is trying to join.
var tailnetLoginURLPattern = regexp.MustCompile(`https://login\.tailscale\.com/[^\s"']+`)

// KiroCrewReconciler reconciles a KiroCrew object.
type KiroCrewReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// Clientset reads pod logs, which is how an outstanding tailnet login URL is
	// surfaced. Optional: without it the operator simply reports no login URL.
	Clientset kubernetes.Interface

	// TailnetDomain is the tailnet's MagicDNS suffix (for example
	// "example-tailnet.ts.net"). It is operator-level configuration because every
	// instance shares one tailnet, and it is what lets the operator publish the
	// HTTPS address the desktop app connects to.
	TailnetDomain string
}

// +kubebuilder:rbac:groups=kirocrew.lsdopen.io,resources=kirocrews,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kirocrew.lsdopen.io,resources=kirocrews/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=serviceaccounts,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=core,resources=configmaps,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=core,resources=persistentvolumeclaims,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=pods/log,verbs=get
// +kubebuilder:rbac:groups=networking.k8s.io,resources=networkpolicies,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=core,resources=events,verbs=create;patch

// Reconcile drives one KiroCrew instance toward its desired state.
func (r *KiroCrewReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var crew kirocrewv1alpha1.KiroCrew
	if err := r.Get(ctx, req.NamespacedName, &crew); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Deletion needs no finalizer: owner references garbage-collect every
	// generated object, and the operator holds no external state to unwind — it
	// writes no tailnet ACLs and no cloud resources. A finalizer here would only
	// be able to stall deletion while the operator is unavailable.
	if !crew.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	if err := r.reconcileServiceAccount(ctx, &crew); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconciling ServiceAccount: %w", err)
	}
	if err := r.reconcileConfigMap(ctx, &crew); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconciling ConfigMap: %w", err)
	}
	if err := r.reconcileNetworkPolicy(ctx, &crew); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconciling NetworkPolicy: %w", err)
	}
	if err := r.reconcileStatefulSet(ctx, &crew); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconciling StatefulSet: %w", err)
	}

	if err := r.reconcileStatus(ctx, &crew); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconciling status: %w", err)
	}

	logger.V(1).Info("Reconciled KiroCrew", "owner", crew.Spec.Owner, "size", crew.Spec.Size)

	// While the node is still waiting for its owner's one-time authentication the
	// login URL only exists in the sidecar's output, so poll until it is joined.
	if crew.Status.TailnetLoginURL != "" {
		return ctrl.Result{RequeueAfter: tailnetPollInterval}, nil
	}
	return ctrl.Result{}, nil
}

func instanceName(crew *kirocrewv1alpha1.KiroCrew) string {
	return fmt.Sprintf("%s-%s", namePrefix, crew.Name)
}

func labelsFor(crew *kirocrewv1alpha1.KiroCrew) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":       namePrefix,
		"app.kubernetes.io/instance":   crew.Name,
		"app.kubernetes.io/managed-by": "kiro-crew-operator",
	}
}

// resolveSize returns the pod shape for the instance's tier, honouring an
// explicit Resources override.
func resolveSize(crew *kirocrewv1alpha1.KiroCrew) (corev1.ResourceRequirements, resource.Quantity) {
	size := crew.Spec.Size
	if size == "" {
		size = kirocrewv1alpha1.SizeLight
	}
	profile, ok := kirocrewv1alpha1.SizeProfiles[size]
	if !ok {
		profile = kirocrewv1alpha1.SizeProfiles[kirocrewv1alpha1.SizeLight]
	}

	resources := profile.Resolve()
	if crew.Spec.Gateway.Resources != nil {
		resources = *crew.Spec.Gateway.Resources
	}

	storage := resource.MustParse(profile.Storage)
	if crew.Spec.Storage.Size != nil {
		storage = *crew.Spec.Storage.Size
	}
	return resources, storage
}

// reconcileConfigMap delivers the gateway's config file.
//
// dashboard.tailscale.enabled is what makes the gateway derive and trust its own
// tailnet origin at startup. Without it the dashboard answers 403 to a request
// arriving on the MagicDNS name, so the desktop app could reach the pod and still
// be refused. Config travels in a ConfigMap rather than the image so it can change
// with a redeploy instead of a rebuild.
func (r *KiroCrewReconciler) reconcileConfigMap(ctx context.Context, crew *kirocrewv1alpha1.KiroCrew) error {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      instanceName(crew) + "-config",
			Namespace: crew.Namespace,
		},
	}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, cm, func() error {
		cm.Labels = labelsFor(crew)
		cm.Data = map[string]string{
			"config.json": `{
  "dashboard": {
    "tailscale": {
      "enabled": true
    }
  }
}
`,
		}
		return controllerutil.SetControllerReference(crew, cm, r.Scheme)
	})
	return err
}

func (r *KiroCrewReconciler) reconcileServiceAccount(ctx context.Context, crew *kirocrewv1alpha1.KiroCrew) error {
	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      instanceName(crew),
			Namespace: crew.Namespace,
		},
	}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, sa, func() error {
		sa.Labels = labelsFor(crew)
		if crew.Spec.AWS.RoleARN != "" {
			if sa.Annotations == nil {
				sa.Annotations = map[string]string{}
			}
			sa.Annotations["eks.amazonaws.com/role-arn"] = crew.Spec.AWS.RoleARN
		}
		return controllerutil.SetControllerReference(crew, sa, r.Scheme)
	})
	return err
}

// reconcileNetworkPolicy denies all pod-to-pod ingress to the instance.
//
// Defence in depth rather than the primary control: the gateway already binds
// loopback only, so this closes the pod network as well and keeps one crew from
// reaching another. Egress is deliberately unrestricted — crews call Bedrock,
// MCP servers and the wider internet.
func (r *KiroCrewReconciler) reconcileNetworkPolicy(ctx context.Context, crew *kirocrewv1alpha1.KiroCrew) error {
	np := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      instanceName(crew),
			Namespace: crew.Namespace,
		},
	}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, np, func() error {
		np.Labels = labelsFor(crew)
		np.Spec = networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: labelsFor(crew)},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			// No ingress rules: nothing on the pod network may connect. Access
			// arrives over WireGuard inside the pod, not through the cluster.
			Ingress: []networkingv1.NetworkPolicyIngressRule{},
		}
		return controllerutil.SetControllerReference(crew, np, r.Scheme)
	})
	return err
}

// reconcileStatefulSet builds the instance's pod.
//
// A StatefulSet rather than a Deployment: the instance is a single stateful
// replica bound to one PVC, and volumeClaimTemplates give a stable claim without
// the operator managing PVC lifecycle by hand.
func (r *KiroCrewReconciler) reconcileStatefulSet(ctx context.Context, crew *kirocrewv1alpha1.KiroCrew) error {
	resources, storage := resolveSize(crew)
	labels := labelsFor(crew)

	gatewayImage := crew.Spec.Gateway.Image
	if gatewayImage == "" {
		gatewayImage = defaultGatewayImage
	}
	tailscaleImage := crew.Spec.Tailscale.Image
	if tailscaleImage == "" {
		tailscaleImage = defaultTailscaleImage
	}
	hostname := crew.Spec.Tailscale.Hostname
	if hostname == "" {
		hostname = instanceName(crew)
	}

	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      instanceName(crew),
			Namespace: crew.Namespace,
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, sts, func() error {
		sts.Labels = labels
		replicas := int32(1)
		runAsNonRoot := true
		gatewayUID := gatewayUIDValue
		gatewayGID := gatewayUIDValue
		allowPrivilegeEscalation := false
		readOnlyRootFS := false // the gateway writes scratch under KIROCREW_HOME

		sts.Spec.Replicas = &replicas
		sts.Spec.ServiceName = instanceName(crew)
		sts.Spec.Selector = &metav1.LabelSelector{MatchLabels: labels}

		sts.Spec.Template.Labels = labels
		sts.Spec.Template.Spec.ServiceAccountName = instanceName(crew)
		// The upstream image runs as uid/gid 1000 and keeps every piece of state
		// on the volume, so without fsGroup a freshly provisioned block volume
		// comes up root-owned and the gateway cannot write its own memory or
		// token vault.
		sts.Spec.Template.Spec.SecurityContext = &corev1.PodSecurityContext{
			RunAsNonRoot: &runAsNonRoot,
			RunAsUser:    &gatewayUID,
			RunAsGroup:   &gatewayGID,
			FSGroup:      &gatewayGID,
		}

		env := []corev1.EnvVar{
			{Name: "KIROCREW_HOME", Value: dataMountPath},
			{Name: "KIROCREW_PORT", Value: fmt.Sprintf("%d", gatewayPort)},
			// The image defaults to 0.0.0.0. Containers in a pod share a network
			// namespace, so the tailscale sidecar still reaches the gateway on
			// loopback, and binding loopback-only means the deny-all
			// NetworkPolicy is not the sole thing keeping the pod network out.
			{Name: "KIROCREW_BIND", Value: "127.0.0.1"},
			// Force the remote install shape. Left to its own detection the
			// gateway sees a container and assumes a loopback OAuth callback can
			// work, but the owner's browser is on their laptop, so that flow
			// dead-ends. The remote shape selects the device-code flow, which
			// needs no callback port and is approved from any browser.
			{Name: "KIRO_AUTH_INSTALL_SHAPE", Value: "remote"},
			{Name: "KIROCREW_OWNER", Value: crew.Spec.Owner},
		}

		gatewayMounts := []corev1.VolumeMount{
			{Name: dataVolumeName, MountPath: dataMountPath},
			{Name: "config", MountPath: configMountPath, ReadOnly: true},
		}
		if crew.Spec.MCPConfigRef != "" {
			gatewayMounts = append(gatewayMounts, corev1.VolumeMount{
				Name:      "mcp-config",
				MountPath: mcpConfigMountPath,
				ReadOnly:  true,
			})
			env = append(env, corev1.EnvVar{
				Name:  "MCP_CONFIG_PATH",
				Value: mcpConfigMountPath + "/mcp-servers.json",
			})
		}

		gateway := corev1.Container{
			Name:            "gateway",
			Image:           gatewayImage,
			ImagePullPolicy: crew.Spec.Gateway.ImagePullPolicy,
			Env:             env,
			Resources:       resources,
			VolumeMounts:    gatewayMounts,
			SecurityContext: &corev1.SecurityContext{
				AllowPrivilegeEscalation: &allowPrivilegeEscalation,
				ReadOnlyRootFilesystem:   &readOnlyRootFS,
				Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
			},
			// No postStart hook publishes the dashboard. `kirocrew tailnet up`
			// shells out to a tailscale CLI at a fixed absolute path and
			// deliberately never consults PATH, and this image ships no such
			// binary — so publishing is the sidecar's job via TS_SERVE_CONFIG
			// below. All this container needs is to trust the tailnet origin,
			// which it reads from config.
			ReadinessProbe: &corev1.Probe{
				ProbeHandler: corev1.ProbeHandler{
					HTTPGet: &corev1.HTTPGetAction{
						Path: gatewayHealthPath,
						Port: intOrString(gatewayPort),
						Host: "127.0.0.1",
					},
				},
				InitialDelaySeconds: 10,
				PeriodSeconds:       15,
			},
		}

		// Tailscale SSH is deliberately not enabled. The dashboard is the
		// interface, reached by the desktop app over the tailnet, and kubectl
		// exec already covers administrative access — so an SSH server on every
		// crew would add a second way into a pod holding a user's Kiro token
		// without adding a capability anyone needs.
		tailscaleEnv := []corev1.EnvVar{
			{Name: "TS_HOSTNAME", Value: hostname},
			{Name: "TS_STATE_DIR", Value: tailscaleStatePath},
			{Name: "TS_USERSPACE", Value: "true"},
		}

		tailscale := corev1.Container{
			Name:  tailscaleContainerName,
			Image: tailscaleImage,
			Env:   tailscaleEnv,
			VolumeMounts: []corev1.VolumeMount{
				{Name: dataVolumeName, MountPath: tailscaleStatePath, SubPath: tailscaleContainerName},
			},
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("50m"),
					corev1.ResourceMemory: resource.MustParse("64Mi"),
				},
				Limits: corev1.ResourceList{
					corev1.ResourceMemory: resource.MustParse("128Mi"),
				},
			},
		}

		sts.Spec.Template.Spec.Containers = []corev1.Container{gateway, tailscale}

		volumes := []corev1.Volume{
			{
				Name: "config",
				VolumeSource: corev1.VolumeSource{
					ConfigMap: &corev1.ConfigMapVolumeSource{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: instanceName(crew) + "-config",
						},
					},
				},
			},
		}
		if crew.Spec.MCPConfigRef != "" {
			volumes = append(volumes, corev1.Volume{
				Name: "mcp-config",
				VolumeSource: corev1.VolumeSource{
					ConfigMap: &corev1.ConfigMapVolumeSource{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: crew.Spec.MCPConfigRef,
						},
					},
				},
			})
		}
		sts.Spec.Template.Spec.Volumes = volumes

		// volumeClaimTemplates are immutable after creation, so only set them on
		// the initial create; a size change needs the PVC expanded directly.
		if sts.CreationTimestamp.IsZero() {
			sts.Spec.VolumeClaimTemplates = []corev1.PersistentVolumeClaim{
				{
					ObjectMeta: metav1.ObjectMeta{Name: dataVolumeName},
					Spec: corev1.PersistentVolumeClaimSpec{
						AccessModes: []corev1.PersistentVolumeAccessMode{
							// One writer, and block storage: Kiro Crew's SQLite
							// databases run in WAL mode, which is unsupported on
							// a network filesystem.
							corev1.ReadWriteOnce,
						},
						StorageClassName: crew.Spec.Storage.StorageClassName,
						Resources: corev1.VolumeResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceStorage: storage,
							},
						},
					},
				},
			}
		}

		return controllerutil.SetControllerReference(crew, sts, r.Scheme)
	})
	return err
}

// reconcileStatus reports readiness, the tailnet dashboard address, and — while
// the node is still unauthenticated — the login URL its owner must open.
func (r *KiroCrewReconciler) reconcileStatus(ctx context.Context, crew *kirocrewv1alpha1.KiroCrew) error {
	var sts appsv1.StatefulSet
	err := r.Get(ctx, types.NamespacedName{Name: instanceName(crew), Namespace: crew.Namespace}, &sts)
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	podReady := err == nil && sts.Status.ReadyReplicas > 0

	loginURL := r.tailnetLoginURL(ctx, crew)
	joined := loginURL == "" && podReady

	patched := crew.DeepCopy()
	patched.Status.ObservedGeneration = crew.Generation
	patched.Status.TailnetLoginURL = loginURL

	// Only advertise a dashboard address once the node is actually on the tailnet;
	// publishing a MagicDNS name that does not resolve yet would just send the
	// owner to a dead URL.
	if joined && r.TailnetDomain != "" {
		hostname := crew.Spec.Tailscale.Hostname
		if hostname == "" {
			hostname = instanceName(crew)
		}
		patched.Status.DashboardURL = fmt.Sprintf("https://%s.%s", hostname, r.TailnetDomain)
	} else {
		patched.Status.DashboardURL = ""
	}

	tailnetCondition := metav1.Condition{
		Type:               kirocrewv1alpha1.ConditionTailnetJoined,
		Status:             metav1.ConditionFalse,
		Reason:             "AwaitingOwnerAuthentication",
		Message:            "Waiting for the owner to authenticate this node",
		ObservedGeneration: crew.Generation,
	}
	if loginURL != "" {
		tailnetCondition.Message = "Owner must open status.tailnetLoginURL to authenticate this node"
	}
	if joined {
		tailnetCondition.Status = metav1.ConditionTrue
		tailnetCondition.Reason = "Joined"
		tailnetCondition.Message = "Node is on the tailnet"
	}
	upsertCondition(&patched.Status.Conditions, tailnetCondition)

	readyCondition := metav1.Condition{
		Type:               kirocrewv1alpha1.ConditionReady,
		Status:             metav1.ConditionFalse,
		Reason:             "PodNotReady",
		Message:            "Waiting for the gateway pod to become ready",
		ObservedGeneration: crew.Generation,
	}
	switch {
	case podReady && joined:
		readyCondition.Status = metav1.ConditionTrue
		readyCondition.Reason = "Serving"
		readyCondition.Message = "Gateway is reachable by its owner on the tailnet"
	case podReady:
		readyCondition.Reason = "TailnetNotJoined"
		readyCondition.Message = "Gateway is running but the node is not yet authenticated"
	}
	upsertCondition(&patched.Status.Conditions, readyCondition)

	return r.Status().Patch(ctx, patched, client.MergeFrom(crew))
}

// tailnetLoginURL looks for an outstanding interactive-login URL in the tailscale
// sidecar's output. An empty string means there is none to surface — either the
// node is already authenticated, or its logs are not readable yet.
func (r *KiroCrewReconciler) tailnetLoginURL(ctx context.Context, crew *kirocrewv1alpha1.KiroCrew) string {
	if r.Clientset == nil {
		return ""
	}
	logger := log.FromContext(ctx)

	var pods corev1.PodList
	if err := r.List(ctx, &pods,
		client.InNamespace(crew.Namespace),
		client.MatchingLabels(labelsFor(crew)),
	); err != nil {
		logger.V(1).Info("Could not list Pods for tailnet login URL", "error", err)
		return ""
	}
	if len(pods.Items) == 0 {
		return ""
	}

	tail := int64(200)
	raw, err := r.Clientset.CoreV1().
		Pods(crew.Namespace).
		GetLogs(pods.Items[0].Name, &corev1.PodLogOptions{
			Container: tailscaleContainerName,
			TailLines: &tail,
		}).
		DoRaw(ctx)
	if err != nil {
		logger.V(1).Info("Could not read tailscale logs", "error", err)
		return ""
	}

	// The last occurrence wins: an earlier, already-consumed URL may still be in
	// the log tail after a restart.
	matches := tailnetLoginURLPattern.FindAllString(string(raw), -1)
	if len(matches) == 0 {
		return ""
	}
	return matches[len(matches)-1]
}

func upsertCondition(conditions *[]metav1.Condition, next metav1.Condition) {
	next.LastTransitionTime = metav1.Now()
	for i := range *conditions {
		if (*conditions)[i].Type == next.Type {
			if (*conditions)[i].Status == next.Status {
				next.LastTransitionTime = (*conditions)[i].LastTransitionTime
			}
			(*conditions)[i] = next
			return
		}
	}
	*conditions = append(*conditions, next)
}

// SetupWithManager registers the controller.
func (r *KiroCrewReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kirocrewv1alpha1.KiroCrew{}).
		Owns(&appsv1.StatefulSet{}).
		Owns(&corev1.ServiceAccount{}).
		Owns(&networkingv1.NetworkPolicy{}).
		Named("kirocrew").
		Complete(r)
}
