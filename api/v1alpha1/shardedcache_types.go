package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ShardedCacheSpec defines the desired state of a ShardedCache.
type ShardedCacheSpec struct {
	// Replicas is the number of cache shards to deploy. Each shard is a separate
	// pod that owns a subset of the key space. Minimum is 1.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=1
	Replicas int32 `json:"replicas"`

	// Image is the container image used for each cache shard.
	// Example: "redis:7"
	// +kubebuilder:validation:MinLength=1
	Image string `json:"image"`

	// Resources describes the compute resource requirements for each cache shard.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`
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
// +kubebuilder:printcolumn:name="Replicas",type=integer,JSONPath=`.spec.replicas`
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
