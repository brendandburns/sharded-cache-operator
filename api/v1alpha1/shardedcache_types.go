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

// ShardedCacheSpec defines the desired state of a ShardedCache.
// Only two knobs are exposed: how many shards and how large each shard should
// be. All other implementation details (cache software, image version, ports,
// storage) are managed by the operator.
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
// Kubernetes workload. Each shard can be addressed individually via the operator-
// managed headless Service, allowing clients to route requests using consistent
// hashing or any other sharding strategy.
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
