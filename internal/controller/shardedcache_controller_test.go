package controller_test

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	cachev1alpha1 "github.com/brendandburns/sharded-cache-operator/api/v1alpha1"
	"github.com/brendandburns/sharded-cache-operator/internal/controller"
)

// newScheme returns a runtime.Scheme with all required types registered.
func newScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := cachev1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("AddToScheme cachev1alpha1: %v", err)
	}
	if err := appsv1.AddToScheme(s); err != nil {
		t.Fatalf("AddToScheme appsv1: %v", err)
	}
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatalf("AddToScheme corev1: %v", err)
	}
	return s
}

// sampleSC returns a minimal ShardedCache with the given shard count and size.
func sampleSC(name, ns string, shards int32, size cachev1alpha1.CacheSize) *cachev1alpha1.ShardedCache {
	return &cachev1alpha1.ShardedCache{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: cachev1alpha1.ShardedCacheSpec{
			Shards: shards,
			Size:   size,
		},
	}
}

// TestReconcile_CreatesStatefulSetAndService validates that the first reconcile
// for a new ShardedCache creates both the StatefulSet and the headless Service.
func TestReconcile_CreatesStatefulSetAndService(t *testing.T) {
	const (
		name   = "myredis"
		ns     = "default"
		shards = int32(3)
	)
	sc := sampleSC(name, ns, shards, cachev1alpha1.CacheSizeSmall)
	scheme := newScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(sc).WithObjects(sc).Build()
	r := &controller.ShardedCacheReconciler{Client: c, Scheme: scheme}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: name, Namespace: ns},
	})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	// Expect a StatefulSet with the correct shard count and the default cache image.
	var sts appsv1.StatefulSet
	if err := c.Get(context.Background(), types.NamespacedName{Name: name, Namespace: ns}, &sts); err != nil {
		t.Fatalf("StatefulSet not found: %v", err)
	}
	if sts.Spec.Replicas == nil || *sts.Spec.Replicas != shards {
		t.Errorf("expected %d replicas, got %v", shards, sts.Spec.Replicas)
	}
	if got := sts.Spec.Template.Spec.Containers[0].Image; got != "redis:7" {
		t.Errorf("expected default image redis:7, got %q", got)
	}

	// Expect a headless Service.
	var svc corev1.Service
	if err := c.Get(context.Background(), types.NamespacedName{Name: name, Namespace: ns}, &svc); err != nil {
		t.Fatalf("Service not found: %v", err)
	}
	if svc.Spec.ClusterIP != "None" {
		t.Errorf("expected headless Service (ClusterIP=None), got %q", svc.Spec.ClusterIP)
	}
}

// TestReconcile_UpdatesShardsOnSpecChange validates that changing spec.shards
// causes the StatefulSet to be updated on the next reconcile pass.
func TestReconcile_UpdatesShardsOnSpecChange(t *testing.T) {
	const (
		name = "cache"
		ns   = "default"
	)
	sc := sampleSC(name, ns, 2, cachev1alpha1.CacheSizeSmall)
	scheme := newScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(sc).WithObjects(sc).Build()
	r := &controller.ShardedCacheReconciler{Client: c, Scheme: scheme}
	key := types.NamespacedName{Name: name, Namespace: ns}

	// First reconcile – creates child resources.
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}

	// Bump the shard count in the ShardedCache spec.
	var live cachev1alpha1.ShardedCache
	if err := c.Get(context.Background(), key, &live); err != nil {
		t.Fatalf("Get ShardedCache: %v", err)
	}
	live.Spec.Shards = 5
	if err := c.Update(context.Background(), &live); err != nil {
		t.Fatalf("Update ShardedCache: %v", err)
	}

	// Second reconcile – should propagate the new shard count.
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}

	var sts appsv1.StatefulSet
	if err := c.Get(context.Background(), key, &sts); err != nil {
		t.Fatalf("StatefulSet not found: %v", err)
	}
	if sts.Spec.Replicas == nil || *sts.Spec.Replicas != 5 {
		t.Errorf("expected 5 replicas after update, got %v", sts.Spec.Replicas)
	}
}

// TestReconcile_NotFound validates that reconciling a deleted ShardedCache
// is a no-op (returns nil without error).
func TestReconcile_NotFound(t *testing.T) {
	scheme := newScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := &controller.ShardedCacheReconciler{Client: c, Scheme: scheme}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "gone", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("expected no error for missing resource, got %v", err)
	}
}

// TestReconcile_SmallSizeResources validates that "small" size sets the
// expected resource requests/limits on the cache container.
func TestReconcile_SmallSizeResources(t *testing.T) {
	const (
		name = "restest"
		ns   = "default"
	)
	sc := sampleSC(name, ns, 1, cachev1alpha1.CacheSizeSmall)
	scheme := newScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(sc).WithObjects(sc).Build()
	r := &controller.ShardedCacheReconciler{Client: c, Scheme: scheme}
	key := types.NamespacedName{Name: name, Namespace: ns}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	var sts appsv1.StatefulSet
	if err := c.Get(context.Background(), key, &sts); err != nil {
		t.Fatalf("StatefulSet not found: %v", err)
	}
	ctr := sts.Spec.Template.Spec.Containers[0]
	memReq, ok := ctr.Resources.Requests[corev1.ResourceMemory]
	if !ok {
		t.Fatal("memory request missing for small size")
	}
	if memReq.Cmp(resource.MustParse("64Mi")) != 0 {
		t.Errorf("small memory request = %v, want 64Mi", memReq)
	}
}

// TestReconcile_MediumSizeResources validates that "medium" size sets higher
// resource requests/limits than "small".
func TestReconcile_MediumSizeResources(t *testing.T) {
	const (
		name = "medtest"
		ns   = "default"
	)
	sc := sampleSC(name, ns, 1, cachev1alpha1.CacheSizeMedium)
	scheme := newScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(sc).WithObjects(sc).Build()
	r := &controller.ShardedCacheReconciler{Client: c, Scheme: scheme}
	key := types.NamespacedName{Name: name, Namespace: ns}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	var sts appsv1.StatefulSet
	if err := c.Get(context.Background(), key, &sts); err != nil {
		t.Fatalf("StatefulSet not found: %v", err)
	}
	ctr := sts.Spec.Template.Spec.Containers[0]
	memReq, ok := ctr.Resources.Requests[corev1.ResourceMemory]
	if !ok {
		t.Fatal("memory request missing for medium size")
	}
	if memReq.Cmp(resource.MustParse("128Mi")) != 0 {
		t.Errorf("medium memory request = %v, want 128Mi", memReq)
	}
}

// TestReconcile_SizeChangeUpdatesResources validates that changing spec.size
// causes the StatefulSet resources to be updated on the next reconcile.
func TestReconcile_SizeChangeUpdatesResources(t *testing.T) {
	const (
		name = "sizechange"
		ns   = "default"
	)
	sc := sampleSC(name, ns, 1, cachev1alpha1.CacheSizeSmall)
	scheme := newScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(sc).WithObjects(sc).Build()
	r := &controller.ShardedCacheReconciler{Client: c, Scheme: scheme}
	key := types.NamespacedName{Name: name, Namespace: ns}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}

	var live cachev1alpha1.ShardedCache
	if err := c.Get(context.Background(), key, &live); err != nil {
		t.Fatalf("Get ShardedCache: %v", err)
	}
	live.Spec.Size = cachev1alpha1.CacheSizeLarge
	if err := c.Update(context.Background(), &live); err != nil {
		t.Fatalf("Update ShardedCache: %v", err)
	}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}

	var sts appsv1.StatefulSet
	if err := c.Get(context.Background(), key, &sts); err != nil {
		t.Fatalf("StatefulSet not found: %v", err)
	}
	memReq, ok := sts.Spec.Template.Spec.Containers[0].Resources.Requests[corev1.ResourceMemory]
	if !ok {
		t.Fatal("memory request missing after size change to large")
	}
	if memReq.Cmp(resource.MustParse("256Mi")) != 0 {
		t.Errorf("large memory request = %v, want 256Mi", memReq)
	}
}

// TestReconcile_LabelsApplied validates that the standard label set is applied
// to both the StatefulSet and the Service.
func TestReconcile_LabelsApplied(t *testing.T) {
	const (
		name = "lbltest"
		ns   = "default"
	)
	sc := sampleSC(name, ns, 1, cachev1alpha1.CacheSizeSmall)
	scheme := newScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(sc).WithObjects(sc).Build()
	r := &controller.ShardedCacheReconciler{Client: c, Scheme: scheme}
	key := types.NamespacedName{Name: name, Namespace: ns}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	var sts appsv1.StatefulSet
	if err := c.Get(context.Background(), key, &sts); err != nil {
		t.Fatalf("StatefulSet not found: %v", err)
	}
	if v := sts.Labels["app.kubernetes.io/instance"]; v != name {
		t.Errorf("StatefulSet label app.kubernetes.io/instance = %q, want %q", v, name)
	}

	var svc corev1.Service
	if err := c.Get(context.Background(), key, &svc); err != nil {
		t.Fatalf("Service not found: %v", err)
	}
	if v := svc.Labels["app.kubernetes.io/managed-by"]; v != "sharded-cache-operator" {
		t.Errorf("Service label app.kubernetes.io/managed-by = %q", v)
	}
}

// TestReconcile_BackendServiceEnvVar validates that when spec.backend is set
// to a Service, the CACHE_UPSTREAM environment variable is injected into the
// cache container with the correct cluster-local URL.
func TestReconcile_BackendServiceEnvVar(t *testing.T) {
	const (
		name = "backendtest"
		ns   = "default"
		port = int32(8080)
	)
	sc := sampleSC(name, ns, 1, cachev1alpha1.CacheSizeSmall)
	sc.Spec.Backend = &cachev1alpha1.BackendRef{
		Kind: cachev1alpha1.BackendKindService,
		Name: "my-app",
		Port: func() *int32 { p := port; return &p }(),
	}
	scheme := newScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(sc).WithObjects(sc).Build()
	r := &controller.ShardedCacheReconciler{Client: c, Scheme: scheme}
	key := types.NamespacedName{Name: name, Namespace: ns}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	var sts appsv1.StatefulSet
	if err := c.Get(context.Background(), key, &sts); err != nil {
		t.Fatalf("StatefulSet not found: %v", err)
	}
	env := sts.Spec.Template.Spec.Containers[0].Env
	var upstream string
	for _, e := range env {
		if e.Name == "CACHE_UPSTREAM" {
			upstream = e.Value
			break
		}
	}
	want := "http://my-app.default.svc.cluster.local:8080"
	if upstream != want {
		t.Errorf("CACHE_UPSTREAM = %q, want %q", upstream, want)
	}
}

// TestReconcile_NoBackendNoEnvVar validates that when spec.backend is absent
// no CACHE_UPSTREAM env var is injected into the cache container.
func TestReconcile_NoBackendNoEnvVar(t *testing.T) {
	const (
		name = "nobackend"
		ns   = "default"
	)
	sc := sampleSC(name, ns, 1, cachev1alpha1.CacheSizeSmall)
	scheme := newScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(sc).WithObjects(sc).Build()
	r := &controller.ShardedCacheReconciler{Client: c, Scheme: scheme}
	key := types.NamespacedName{Name: name, Namespace: ns}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	var sts appsv1.StatefulSet
	if err := c.Get(context.Background(), key, &sts); err != nil {
		t.Fatalf("StatefulSet not found: %v", err)
	}
	for _, e := range sts.Spec.Template.Spec.Containers[0].Env {
		if e.Name == "CACHE_UPSTREAM" {
			t.Errorf("unexpected CACHE_UPSTREAM env var when no backend is set")
		}
	}
}

// TestReconcile_BackendChangeUpdatesEnvVar validates that changing spec.backend
// causes the StatefulSet pod template to be updated on the next reconcile.
func TestReconcile_BackendChangeUpdatesEnvVar(t *testing.T) {
	const (
		name = "backendchange"
		ns   = "default"
	)
	port := int32(9000)
	sc := sampleSC(name, ns, 1, cachev1alpha1.CacheSizeSmall)
	sc.Spec.Backend = &cachev1alpha1.BackendRef{
		Kind: cachev1alpha1.BackendKindService,
		Name: "svc-a",
		Port: &port,
	}
	scheme := newScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(sc).WithObjects(sc).Build()
	r := &controller.ShardedCacheReconciler{Client: c, Scheme: scheme}
	key := types.NamespacedName{Name: name, Namespace: ns}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}

	// Update backend to point to a different service.
	var live cachev1alpha1.ShardedCache
	if err := c.Get(context.Background(), key, &live); err != nil {
		t.Fatalf("Get ShardedCache: %v", err)
	}
	newPort := int32(9001)
	live.Spec.Backend = &cachev1alpha1.BackendRef{
		Kind: cachev1alpha1.BackendKindService,
		Name: "svc-b",
		Port: &newPort,
	}
	if err := c.Update(context.Background(), &live); err != nil {
		t.Fatalf("Update ShardedCache: %v", err)
	}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}

	var sts appsv1.StatefulSet
	if err := c.Get(context.Background(), key, &sts); err != nil {
		t.Fatalf("StatefulSet not found: %v", err)
	}
	var upstream string
	for _, e := range sts.Spec.Template.Spec.Containers[0].Env {
		if e.Name == "CACHE_UPSTREAM" {
			upstream = e.Value
			break
		}
	}
	want := "http://svc-b.default.svc.cluster.local:9001"
	if upstream != want {
		t.Errorf("CACHE_UPSTREAM after update = %q, want %q", upstream, want)
	}
}
