package controller

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	cachev1alpha1 "github.com/brendandburns/sharded-cache-operator/api/v1alpha1"
)

// defaultCacheImage is the container image used for every cache shard.
// Users never need to specify an image; the operator manages this detail.
const defaultCacheImage = "memcached:1"

// defaultCachePort is the port exposed by each cache shard container.
const defaultCachePort = int32(11211)

// ShardedCacheReconciler watches ShardedCache objects and keeps child resources in sync.
type ShardedCacheReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=cache.operators.io,resources=shardedcaches,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cache.operators.io,resources=shardedcaches/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=cache.operators.io,resources=shardedcaches/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete

// Reconcile is invoked whenever a ShardedCache or one of its owned resources changes.
func (r *ShardedCacheReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	lgr := log.FromContext(ctx)

	sc := &cachev1alpha1.ShardedCache{}
	if err := r.Get(ctx, req.NamespacedName, sc); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get ShardedCache %s: %w", req, err)
	}

	if err := r.applyHeadlessService(ctx, sc); err != nil {
		return ctrl.Result{}, err
	}

	if sc.Spec.Backend != nil &&
		(sc.Spec.Backend.Kind == cachev1alpha1.BackendKindService || sc.Spec.Backend.Kind == "") &&
		sc.Spec.Backend.Port == nil {
		lgr.Info("backend kind is Service but no port specified; assuming port 80",
			"backend", sc.Spec.Backend.Name)
	}

	sts, err := r.applyStatefulSet(ctx, sc)
	if err != nil {
		return ctrl.Result{}, err
	}

	lgr.Info("reconciled", "shards", sc.Spec.Shards, "ready", sts.Status.ReadyReplicas)
	return ctrl.Result{}, r.refreshStatus(ctx, sc, sts)
}

// applyHeadlessService reconciles the headless Service for the cache shards.
func (r *ShardedCacheReconciler) applyHeadlessService(ctx context.Context, sc *cachev1alpha1.ShardedCache) error {
	want := r.buildHeadlessService(sc)
	if err := controllerutil.SetControllerReference(sc, want, r.Scheme); err != nil {
		return err
	}
	got := &corev1.Service{}
	switch err := r.Get(ctx, client.ObjectKeyFromObject(want), got); {
	case apierrors.IsNotFound(err):
		return r.Create(ctx, want)
	case err != nil:
		return err
	}
	patch := client.MergeFrom(got.DeepCopy())
	got.Spec.Selector = want.Spec.Selector
	got.Spec.Ports = want.Spec.Ports
	return r.Patch(ctx, got, patch)
}

// applyStatefulSet reconciles the StatefulSet for the cache shards.
func (r *ShardedCacheReconciler) applyStatefulSet(ctx context.Context, sc *cachev1alpha1.ShardedCache) (*appsv1.StatefulSet, error) {
	want := r.buildStatefulSet(sc)
	if err := controllerutil.SetControllerReference(sc, want, r.Scheme); err != nil {
		return nil, err
	}
	got := &appsv1.StatefulSet{}
	switch err := r.Get(ctx, client.ObjectKeyFromObject(want), got); {
	case apierrors.IsNotFound(err):
		return want, r.Create(ctx, want)
	case err != nil:
		return nil, err
	}
	if r.statefulSetDrifted(got, want) {
		patch := client.MergeFrom(got.DeepCopy())
		got.Spec.Replicas = want.Spec.Replicas
		got.Spec.Template = want.Spec.Template
		return got, r.Patch(ctx, got, patch)
	}
	return got, nil
}

// refreshStatus updates the ShardedCache status subresource to reflect sts.
func (r *ShardedCacheReconciler) refreshStatus(ctx context.Context, sc *cachev1alpha1.ShardedCache, sts *appsv1.StatefulSet) error {
	prevStatus := sc.Status.DeepCopy()
	sc.Status.ReadyReplicas = sts.Status.ReadyReplicas
	r.upsertAvailableCondition(sc)
	if equality.Semantic.DeepEqual(sc.Status, *prevStatus) {
		return nil
	}
	return r.Status().Update(ctx, sc)
}

// upsertAvailableCondition sets the Available condition on sc based on ready replicas.
func (r *ShardedCacheReconciler) upsertAvailableCondition(sc *cachev1alpha1.ShardedCache) {
	newCond := metav1.Condition{
		Type:               cachev1alpha1.ConditionAvailable,
		ObservedGeneration: sc.Generation,
		LastTransitionTime: metav1.Now(),
	}
	if sc.Status.ReadyReplicas >= sc.Spec.Shards {
		newCond.Status = metav1.ConditionTrue
		newCond.Reason = "AllShardsReady"
		newCond.Message = fmt.Sprintf("all %d shard(s) are ready", sc.Spec.Shards)
	} else {
		newCond.Status = metav1.ConditionFalse
		newCond.Reason = "ShardsNotReady"
		newCond.Message = fmt.Sprintf("%d/%d shard(s) ready", sc.Status.ReadyReplicas, sc.Spec.Shards)
	}
	for i, c := range sc.Status.Conditions {
		if c.Type != newCond.Type {
			continue
		}
		if c.Status == newCond.Status {
			newCond.LastTransitionTime = c.LastTransitionTime
		}
		sc.Status.Conditions[i] = newCond
		return
	}
	sc.Status.Conditions = append(sc.Status.Conditions, newCond)
}

// SetupWithManager registers the reconciler with the controller manager.
func (r *ShardedCacheReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&cachev1alpha1.ShardedCache{}).
		Owns(&appsv1.StatefulSet{}).
		Owns(&corev1.Service{}).
		Complete(r)
}

// buildHeadlessService returns the headless Service spec for sc.
// A ClusterIP=None Service paired with the StatefulSet allows per-shard DNS:
//
//	<pod>.<svc>.<ns>.svc.cluster.local
func (r *ShardedCacheReconciler) buildHeadlessService(sc *cachev1alpha1.ShardedCache) *corev1.Service {
	lbls := shardLabels(sc)
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: sc.Name, Namespace: sc.Namespace, Labels: lbls},
		Spec: corev1.ServiceSpec{
			ClusterIP: "None",
			Selector:  lbls,
			Ports: []corev1.ServicePort{{
				Name:       "cache",
				Port:       defaultCachePort,
				TargetPort: intstr.FromInt32(defaultCachePort),
				Protocol:   corev1.ProtocolTCP,
			}},
		},
	}
}

// buildStatefulSet returns the StatefulSet spec for sc.
func (r *ShardedCacheReconciler) buildStatefulSet(sc *cachev1alpha1.ShardedCache) *appsv1.StatefulSet {
	lbls := shardLabels(sc)
	n := sc.Spec.Shards
	env := upstreamEnv(sc)
	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: sc.Name, Namespace: sc.Namespace, Labels: lbls},
		Spec: appsv1.StatefulSetSpec{
			Replicas:    &n,
			ServiceName: sc.Name,
			Selector:    &metav1.LabelSelector{MatchLabels: lbls},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: lbls},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:      "cache",
						Image:     defaultCacheImage,
						Args:      argsForSize(sc.Spec.Size),
						Env:       env,
						Resources: resourcesForSize(sc.Spec.Size),
						Ports: []corev1.ContainerPort{{
							Name:          "cache",
							ContainerPort: defaultCachePort,
							Protocol:      corev1.ProtocolTCP,
						}},
					}},
				},
			},
		},
	}
}

// statefulSetDrifted reports whether current's key fields differ from desired.
func (r *ShardedCacheReconciler) statefulSetDrifted(current, desired *appsv1.StatefulSet) bool {
	if current.Spec.Replicas == nil || *current.Spec.Replicas != *desired.Spec.Replicas {
		return true
	}
	cc, dc := current.Spec.Template.Spec.Containers[0], desired.Spec.Template.Spec.Containers[0]
	return !equality.Semantic.DeepEqual(cc.Resources, dc.Resources) ||
		!equality.Semantic.DeepEqual(cc.Env, dc.Env)
}

// shardLabels returns labels applied to all child resources of sc.
func shardLabels(sc *cachev1alpha1.ShardedCache) map[string]string {
	return map[string]string{
		"app.kubernetes.io/managed-by": "sharded-cache-operator",
		"app.kubernetes.io/name":       "sharded-cache",
		"app.kubernetes.io/instance":   sc.Name,
	}
}

// resourcesForSize maps a CacheSize tier to concrete CPU and memory
// requests/limits. This is the single place where the operator translates
// the user-visible size knob into Kubernetes resource quantities.
func resourcesForSize(s cachev1alpha1.CacheSize) corev1.ResourceRequirements {
	switch s {
	case cachev1alpha1.CacheSizeMedium:
		return corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("100m"),
				corev1.ResourceMemory: resource.MustParse("128Mi"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("500m"),
				corev1.ResourceMemory: resource.MustParse("256Mi"),
			},
		}
	case cachev1alpha1.CacheSizeLarge:
		return corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("500m"),
				corev1.ResourceMemory: resource.MustParse("256Mi"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("1"),
				corev1.ResourceMemory: resource.MustParse("512Mi"),
			},
		}
	default: // CacheSizeSmall and any unset value
		return corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("50m"),
				corev1.ResourceMemory: resource.MustParse("64Mi"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("100m"),
				corev1.ResourceMemory: resource.MustParse("128Mi"),
			},
		}
	}
}

// argsForSize returns the memcached command-line arguments that configure the
// per-shard memory limit to match the chosen size tier.
func argsForSize(s cachev1alpha1.CacheSize) []string {
	switch s {
	case cachev1alpha1.CacheSizeMedium:
		return []string{"-m", "128"}
	case cachev1alpha1.CacheSizeLarge:
		return []string{"-m", "256"}
	default: // CacheSizeSmall and any unset value
		return []string{"-m", "64"}
	}
}

// upstreamEnv returns the environment variables that expose the backend
// upstream address to the cache container. If no backend is configured an
// empty slice is returned.
func upstreamEnv(sc *cachev1alpha1.ShardedCache) []corev1.EnvVar {
	url := upstreamURL(sc)
	if url == "" {
		return nil
	}
	return []corev1.EnvVar{{Name: "CACHE_UPSTREAM", Value: url}}
}

// upstreamURL derives a cluster-local URL string from sc.Spec.Backend.
// Returns an empty string when Backend is nil.
func upstreamURL(sc *cachev1alpha1.ShardedCache) string {
	b := sc.Spec.Backend
	if b == nil {
		return ""
	}
	ns := b.Namespace
	if ns == "" {
		ns = sc.Namespace
	}
	switch b.Kind {
	case cachev1alpha1.BackendKindIngress:
		return fmt.Sprintf("http://%s.%s", b.Name, ns)
	case cachev1alpha1.BackendKindGateway:
		return fmt.Sprintf("http://%s.%s", b.Name, ns)
	default: // BackendKindService (and empty/default)
		if b.Port != nil {
			return fmt.Sprintf("http://%s.%s.svc.cluster.local:%d", b.Name, ns, *b.Port)
		}
		return fmt.Sprintf("http://%s.%s.svc.cluster.local", b.Name, ns)
	}
}
