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

// sampleSC returns a minimal ShardedCache for use in tests.
func sampleSC(name, ns string, replicas int32, image string) *cachev1alpha1.ShardedCache {
	return &cachev1alpha1.ShardedCache{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: cachev1alpha1.ShardedCacheSpec{
			Replicas: replicas,
			Image:    image,
		},
	}
}

// TestReconcile_CreatesStatefulSetAndService validates that the first reconcile
// for a new ShardedCache creates both the StatefulSet and the headless Service.
func TestReconcile_CreatesStatefulSetAndService(t *testing.T) {
	const (
		name     = "myredis"
		ns       = "default"
		replicas = int32(3)
	)
	sc := sampleSC(name, ns, replicas, "redis:7")
	scheme := newScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(sc).WithObjects(sc).Build()
	r := &controller.ShardedCacheReconciler{Client: c, Scheme: scheme}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: name, Namespace: ns},
	})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	// Expect a StatefulSet with the correct replica count and image.
	var sts appsv1.StatefulSet
	if err := c.Get(context.Background(), types.NamespacedName{Name: name, Namespace: ns}, &sts); err != nil {
		t.Fatalf("StatefulSet not found: %v", err)
	}
	if sts.Spec.Replicas == nil || *sts.Spec.Replicas != replicas {
		t.Errorf("expected %d replicas, got %v", replicas, sts.Spec.Replicas)
	}
	if got := sts.Spec.Template.Spec.Containers[0].Image; got != "redis:7" {
		t.Errorf("unexpected image %q", got)
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

// TestReconcile_UpdatesReplicasOnSpecChange validates that changing spec.replicas
// causes the StatefulSet to be updated on the next reconcile pass.
func TestReconcile_UpdatesReplicasOnSpecChange(t *testing.T) {
	const (
		name = "cache"
		ns   = "default"
	)
	sc := sampleSC(name, ns, 2, "redis:7")
	scheme := newScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(sc).WithObjects(sc).Build()
	r := &controller.ShardedCacheReconciler{Client: c, Scheme: scheme}
	key := types.NamespacedName{Name: name, Namespace: ns}

	// First reconcile – creates child resources.
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}

	// Bump the replica count in the ShardedCache spec.
	var live cachev1alpha1.ShardedCache
	if err := c.Get(context.Background(), key, &live); err != nil {
		t.Fatalf("Get ShardedCache: %v", err)
	}
	live.Spec.Replicas = 5
	if err := c.Update(context.Background(), &live); err != nil {
		t.Fatalf("Update ShardedCache: %v", err)
	}

	// Second reconcile – should propagate the new replica count.
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

// TestReconcile_UpdatesImageOnSpecChange validates that changing spec.image
// is propagated to the StatefulSet pod template.
func TestReconcile_UpdatesImageOnSpecChange(t *testing.T) {
	const (
		name = "imgcache"
		ns   = "default"
	)
	sc := sampleSC(name, ns, 1, "redis:6")
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
	live.Spec.Image = "redis:7"
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
	if got := sts.Spec.Template.Spec.Containers[0].Image; got != "redis:7" {
		t.Errorf("image not updated: got %q, want redis:7", got)
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

// TestReconcile_ResourcesPropagate validates that spec.resources are passed
// through to the cache container in the StatefulSet.
func TestReconcile_ResourcesPropagate(t *testing.T) {
	const (
		name = "restest"
		ns   = "default"
	)
	sc := sampleSC(name, ns, 1, "redis:7")
	sc.Spec.Resources = corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceMemory: resource.MustParse("128Mi"),
		},
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
	container := sts.Spec.Template.Spec.Containers[0]
	memReq, ok := container.Resources.Requests[corev1.ResourceMemory]
	if !ok {
		t.Fatal("memory request not found in StatefulSet container")
	}
	if memReq.Cmp(resource.MustParse("128Mi")) != 0 {
		t.Errorf("memory request = %v, want 128Mi", memReq)
	}
}

// TestReconcile_LabelsApplied validates that the standard label set is applied
// to both the StatefulSet and the Service.
func TestReconcile_LabelsApplied(t *testing.T) {
	const (
		name = "lbltest"
		ns   = "default"
	)
	sc := sampleSC(name, ns, 1, "redis:7")
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
