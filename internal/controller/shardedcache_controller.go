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
// nginx:alpine acts as a transparent HTTP caching reverse proxy; it intercepts
// every HTTP request, serves the response from its local cache when available,
// and forwards cache-miss requests to the configured backend upstream.
const defaultCacheImage = "nginx:alpine"

// defaultCachePort is the HTTP port exposed by each cache shard.
const defaultCachePort = int32(80)

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
// +kubebuilder:rbac:groups=core,resources=configmaps,verbs=get;list;watch;create;update;patch;delete

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

	if err := r.applyConfigMap(ctx, sc); err != nil {
		return ctrl.Result{}, err
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

// applyConfigMap reconciles the nginx ConfigMap that holds the HTTP proxy configuration.
func (r *ShardedCacheReconciler) applyConfigMap(ctx context.Context, sc *cachev1alpha1.ShardedCache) error {
	want := r.buildNginxConfigMap(sc)
	if err := controllerutil.SetControllerReference(sc, want, r.Scheme); err != nil {
		return err
	}
	got := &corev1.ConfigMap{}
	switch err := r.Get(ctx, client.ObjectKeyFromObject(want), got); {
	case apierrors.IsNotFound(err):
		return r.Create(ctx, want)
	case err != nil:
		return err
	}
	patch := client.MergeFrom(got.DeepCopy())
	got.Data = want.Data
	return r.Patch(ctx, got, patch)
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
		Owns(&corev1.ConfigMap{}).
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

// buildNginxConfigMap returns the ConfigMap that holds the nginx HTTP proxy
// configuration for the cache shards. The nginx server listens on port 80,
// caches HTTP responses in /tmp/nginx-cache, and proxies cache-miss requests
// to the backend upstream (when configured).
func (r *ShardedCacheReconciler) buildNginxConfigMap(sc *cachev1alpha1.ShardedCache) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      sc.Name + "-nginx-config",
			Namespace: sc.Namespace,
		},
		Data: map[string]string{
			"default.conf": nginxConfigForSize(sc.Spec.Size, upstreamURL(sc)),
		},
	}
}

// nginxConfigForSize returns the nginx default.conf content that configures
// the shard as a transparent HTTP caching reverse proxy.
// max_size is tuned to the size tier; proxy_pass targets the upstream URL when set.
// When no upstream is set nginx returns 503 for all requests.
func nginxConfigForSize(s cachev1alpha1.CacheSize, upstream string) string {
	cacheSize := "64m"
	switch s {
	case cachev1alpha1.CacheSizeMedium:
		cacheSize = "128m"
	case cachev1alpha1.CacheSizeLarge:
		cacheSize = "256m"
	}
	if upstream == "" {
		return "server {\n    listen 80;\n    location / {\n        return 503 \"no backend service configured\\n\";\n    }\n}\n"
	}
	return fmt.Sprintf(
		// proxy_cache_path: store cached objects under /tmp/nginx-cache.
		// keys_zone=10m holds ~80 000 cache keys in shared memory.
		// inactive=60m evicts entries not requested within 60 minutes.
		// max_size is tuned to the ShardedCache size tier.
		"proxy_cache_path /tmp/nginx-cache levels=1:2 keys_zone=http_cache:10m max_size=%s inactive=60m use_temp_path=off;\n\n"+
			"server {\n"+
			"    listen 80;\n"+
			"    location / {\n"+
			"        proxy_pass         %s;\n"+
			"        proxy_cache        http_cache;\n"+
			"        proxy_cache_methods GET HEAD;\n"+
			"        proxy_cache_valid  200 302 10m;\n"+
			"        proxy_cache_valid  404       1m;\n"+
			"        proxy_cache_use_stale error timeout updating http_500 http_502 http_503 http_504;\n"+
			"        proxy_cache_lock   on;\n"+
			"        add_header         X-Cache-Status $upstream_cache_status;\n"+
			"    }\n"+
			"}\n",
		cacheSize, upstream,
	)
}

// buildStatefulSet returns the StatefulSet spec for sc.
func (r *ShardedCacheReconciler) buildStatefulSet(sc *cachev1alpha1.ShardedCache) *appsv1.StatefulSet {
	lbls := shardLabels(sc)
	n := sc.Spec.Shards
	env := upstreamEnv(sc)
	configMapName := sc.Name + "-nginx-config"
	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: sc.Name, Namespace: sc.Namespace, Labels: lbls},
		Spec: appsv1.StatefulSetSpec{
			Replicas:    &n,
			ServiceName: sc.Name,
			Selector:    &metav1.LabelSelector{MatchLabels: lbls},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: lbls},
				Spec: corev1.PodSpec{
					Volumes: []corev1.Volume{{
						Name: "nginx-config",
						VolumeSource: corev1.VolumeSource{
							ConfigMap: &corev1.ConfigMapVolumeSource{
								LocalObjectReference: corev1.LocalObjectReference{
									Name: configMapName,
								},
							},
						},
					}},
					Containers: []corev1.Container{{
						Name:      "cache",
						Image:     defaultCacheImage,
						Env:       env,
						Resources: resourcesForSize(sc.Spec.Size),
						Ports: []corev1.ContainerPort{{
							Name:          "cache",
							ContainerPort: defaultCachePort,
							Protocol:      corev1.ProtocolTCP,
						}},
						VolumeMounts: []corev1.VolumeMount{{
							Name:      "nginx-config",
							MountPath: "/etc/nginx/conf.d/default.conf",
							SubPath:   "default.conf",
							ReadOnly:  true,
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
