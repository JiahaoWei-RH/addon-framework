package agent

import (
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/labels"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	corev1informers "k8s.io/client-go/informers/core/v1"
	corev1lister "k8s.io/client-go/listers/core/v1"
	"k8s.io/component-base/featuregate"
	clusterv1 "open-cluster-management.io/api/cluster/v1"
)

const LocalStorageCapacityIsolation featuregate.Feature = "LocalStorageCapacityIsolation"

type score struct {
	nodeLister        corev1lister.NodeLister
	useRequested      bool
	enablePodOverhead bool
}

func NewScore(nodeInformer corev1informers.NodeInformer) *score {
	return &score{
		nodeLister:        nodeInformer.Lister(),
		useRequested:      false,
		enablePodOverhead: false,
	}
}

func (s *score) getallocatableList() (allocatable clusterv1.ResourceList, err error) {
	nodes, err := s.nodeLister.List(labels.Everything())
	if err != nil {
		return nil, err
	}

	allocatableList := make(map[clusterv1.ResourceName]resource.Quantity)

	for _, node := range nodes {
		if node.Spec.Unschedulable {
			continue
		}

		for key, value := range node.Status.Allocatable {
			if allocatable, exist := allocatableList[clusterv1.ResourceName(key)]; exist {
				allocatable.Add(value)
				allocatableList[clusterv1.ResourceName(key)] = allocatable
			} else {
				allocatableList[clusterv1.ResourceName(key)] = value
			}
		}
	}

	return allocatableList, nil
}

func (r *score) calculatePodResourceRequest(pod *v1.Pod, resource v1.ResourceName) int64 {
	var podRequest int64
	for i := range pod.Spec.Containers {
		container := &pod.Spec.Containers[i]
		value := GetRequestForResource(resource, &container.Resources.Requests, !r.useRequested)
		podRequest += value
	}

	for i := range pod.Spec.InitContainers {
		initContainer := &pod.Spec.InitContainers[i]
		value := GetRequestForResource(resource, &initContainer.Resources.Requests, !r.useRequested)
		if podRequest < value {
			podRequest = value
		}
	}

	// If Overhead is being utilized, add to the total requests for the pod
	if pod.Spec.Overhead != nil && r.enablePodOverhead {
		if quantity, found := pod.Spec.Overhead[resource]; found {
			podRequest += quantity.Value()
		}
	}

	return podRequest
}

func GetRequestForResource(resource v1.ResourceName, requests *v1.ResourceList, nonZero bool) int64 {
	if requests == nil {
		return 0
	}
	switch resource {
	case v1.ResourceCPU:
		// Override if un-set, but not if explicitly set to zero
		if _, found := (*requests)[v1.ResourceCPU]; !found && nonZero {
			return 100
		}
		return requests.Cpu().MilliValue()
	case v1.ResourceMemory:
		// Override if un-set, but not if explicitly set to zero
		if _, found := (*requests)[v1.ResourceMemory]; !found && nonZero {
			return 200 * 1024 * 1024
		}
		return requests.Memory().Value()
	case v1.ResourceEphemeralStorage:
		// if the local storage capacity isolation feature gate is disabled, pods request 0 disk.
		if !utilfeature.DefaultFeatureGate.Enabled(LocalStorageCapacityIsolation) {
			return 0
		}

		quantity, found := (*requests)[v1.ResourceEphemeralStorage]
		if !found {
			return 0
		}
		return quantity.Value()
	default:
		quantity, found := (*requests)[resource]
		if !found {
			return 0
		}
		return quantity.Value()
	}
}
