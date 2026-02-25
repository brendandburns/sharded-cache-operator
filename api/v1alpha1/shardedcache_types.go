package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// CacheSize is a T-shirt size that controls the compute resources allocated to
// each cache shard. The operator translates the chosen size into concrete CPU
// and memory requests/limits so users don't have to think about raw resource
// quantities.
//
// +kubebuilder:validation:Enum=small;medium;large
type CacheSize string

const (
	// CacheSizeSmall allocates minimal resources suitable for development or
	// low-traffic workloads (64Mi memory / 50m CPU request).
	CacheSizeSmall CacheSize = "small"
	// CacheSizeMedium is a balanced profile for moderate traffic
	// (128Mi memory / 100m CPU request).
	CacheSizeMedium CacheSize = "medium"
	// CacheSizeLarge targets high-throughput production workloads
	// (256Mi memory / 500m CPU request).
	CacheSizeLarge CacheSize = "large"
)

// BackendKind enumerates the supported Kubernetes resource kinds that a
// ShardedCache can route cache-miss traffic toward.
//
// +kubebuilder:validation:Enum=Service;Ingress;Gateway
type BackendKind string

const (
	// BackendKindService references a core/v1 Service.
	BackendKindService BackendKind = "Service"
	// BackendKindIngress references a networking.k8s.io/v1 Ingress.
	BackendKindIngress BackendKind = "Ingress"
	// BackendKindGateway references a gateway.networking.k8s.io/v1 Gateway.
	BackendKindGateway BackendKind = "Gateway"
)

// BackendRef identifies the upstream Kubernetes resource that the cache should
// forward cache-miss requests to. The operator uses this reference to construct
// the upstream URL that is exposed to the cache container via the
// CACHE_UPSTREAM environment variable.
type BackendRef struct {
	// Kind is the type of the backend resource.
	// Supported values: Service, Ingress, Gateway.
	// Defaults to Service.
	// +kubebuilder:default=Service
	// +optional
	Kind BackendKind `json:"kind,omitempty"`

	// Name is the name of the backend resource.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Namespace is the namespace of the backend resource.
	// Defaults to the same namespace as the ShardedCache when omitted.
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// Port is the port on the backend Service to forward traffic to.
	// Required when Kind is "Service"; ignored for Ingress and Gateway.
	// When omitted for a Service, port 80 is assumed.
	// +optional
	Port *int32 `json:"port,omitempty"`
}

// ShardedCacheSpec defines the desired state of a ShardedCache.
// Only three knobs are exposed: how many shards, how large each shard should
// be, and where to route traffic. All other implementation details (cache
// software, image version, ports, storage) are managed by the operator.
type ShardedCacheSpec struct {
	// Shards is the number of independent cache shards to run. Each shard
	// holds a distinct slice of the key space and is individually addressable
	// via its stable DNS name:
	//   <name>-<ordinal>.<name>.<namespace>.svc.cluster.local
	// Minimum is 1; defaults to 1.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=1
	// +optional
	Shards int32 `json:"shards,omitempty"`

	// Size controls the compute resources (CPU + memory) allocated to each
	// cache shard. Accepted values are "small", "medium", and "large".
	// Defaults to "small".
	// +kubebuilder:default=small
	// +optional
	Size CacheSize `json:"size,omitempty"`

	// Backend identifies the upstream Kubernetes resource (Service, Ingress,
	// or Gateway) that the cache should forward traffic to on a cache miss.
	// The operator exposes the resolved upstream address to the cache
	// container via the CACHE_UPSTREAM environment variable.
	// +optional
	Backend *BackendRef `json:"backend,omitempty"`
}

// ShardedCacheStatus defines the observed state of a ShardedCache.
type ShardedCacheStatus struct {
	// ReadyReplicas is the number of cache shard pods that are ready.
	// +optional
	ReadyReplicas int32 `json:"readyReplicas,omitempty"`

	// Conditions represent the latest available observations of a ShardedCache's state.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// Condition types for ShardedCache.
const (
	// ConditionAvailable indicates that the required number of shards are ready.
	ConditionAvailable = "Available"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Shards",type=integer,JSONPath=`.spec.shards`
// +kubebuilder:printcolumn:name="Size",type=string,JSONPath=`.spec.size`
// +kubebuilder:printcolumn:name="Ready",type=integer,JSONPath=`.status.readyReplicas`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// ShardedCache is the Schema for the shardedcaches API.
// It represents a horizontally sharded cache deployment sitting in front of a
// Kubernetes workload. Each shard can be addressed individually via the
// operator-managed headless Service, and the optional Backend field tells
// the cache which upstream Service, Ingress, or Gateway to forward
// cache-miss requests to.
type ShardedCache struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ShardedCacheSpec   `json:"spec,omitempty"`
	Status ShardedCacheStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ShardedCacheList contains a list of ShardedCache.
type ShardedCacheList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ShardedCache `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ShardedCache{}, &ShardedCacheList{})
}
