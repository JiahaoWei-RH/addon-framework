package agent

import (
	"fmt"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/labels"
	corev1informers "k8s.io/client-go/informers/core/v1"
	corev1lister "k8s.io/client-go/listers/core/v1"
	clusterv1 "open-cluster-management.io/api/cluster/v1"
)

const MAXSCORE = float64(100)
const MAXCPUCOUNT = float64(100)

// 1TB
const MAXMEMCOUNT = float64(1024 * 1024)

type Score struct {
	nodeLister        corev1lister.NodeLister
	useRequested      bool
	enablePodOverhead bool
	podListener       corev1lister.PodLister
}

func NewScore(nodeInformer corev1informers.NodeInformer, podInformer corev1informers.PodInformer) *Score {
	return &Score{
		nodeLister:        nodeInformer.Lister(),
		podListener:       podInformer.Lister(),
		enablePodOverhead: true,
		useRequested:      true,
	}
}

func (s *Score) calculateValue() (cpuValue int64, memValue int64, err error) {
	cpuAllocInt, err := s.calculateClusterAllocateable(clusterv1.ResourceCPU)
	fmt.Printf("cpuAllocInt: %+v\n", cpuAllocInt)
	if err != nil {
		return 0, 0, err
	}
	memAllocInt, err := s.calculateClusterAllocateable(clusterv1.ResourceMemory)
	fmt.Printf("memAllocInt: %+v\n", memAllocInt)
	if err != nil {
		return 0, 0, err
	}

	// 单位：个
	cpuUsage, err := s.calculatePodResourceRequest(v1.ResourceCPU)
	fmt.Printf("cpuUsage: %+v\n", cpuUsage)
	if err != nil {
		return 0, 0, err
	}
	// 单位：B
	memUsage, err := s.calculatePodResourceRequest(v1.ResourceMemory)
	fmt.Printf("memUsage: %+v\n", memUsage)
	if err != nil {
		return 0, 0, err
	}

	var availableCpu float64
	availableCpu = float64(cpuAllocInt - cpuUsage)
	if availableCpu > MAXCPUCOUNT {
		cpuValue = int64(MAXSCORE)
	} else {
		cpuValue = int64(MAXSCORE / MAXCPUCOUNT * availableCpu)
	}

	var availableMem float64
	// 单位：MB
	availableMem = float64((memAllocInt - memUsage) / (1024 * 1024))
	// > 1TB
	if availableMem > MAXMEMCOUNT {
		memValue = int64(MAXSCORE)
	} else {
		memValue = int64(MAXSCORE / MAXMEMCOUNT * availableMem)
	}

	return cpuValue, memValue, nil
}

// 计算可用的资源，Allocatable是一开始分配的，剪去了一些系统根本的pod，剩下的可用资源
func (s *Score) calculateClusterAllocateable(resourceName clusterv1.ResourceName) (int64, error) {
	nodes, err := s.nodeLister.List(labels.Everything())
	if err != nil {
		return 0, err
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
	quantity := allocatableList[resourceName]
	return quantity.Value(), nil
}

// 这是计算一个pod的resource 使用量，把所有的pod的usage加起来就是总共使用量，拿allocateable减去使用量就是可用量
func (s *Score) calculatePodResourceRequest(resourceName v1.ResourceName) (int64, error) {
	list, err := s.podListener.List(labels.Everything())
	if err != nil {
		return 0, err
	}

	var podRequest int64
	var podCount int
	for _, pod := range list {

		for i := range pod.Spec.Containers {
			container := &pod.Spec.Containers[i]
			value := s.getRequestForResource(resourceName, &container.Resources.Requests, !s.useRequested)
			podRequest += value
		}

		for i := range pod.Spec.InitContainers {
			initContainer := &pod.Spec.InitContainers[i]
			value := s.getRequestForResource(resourceName, &initContainer.Resources.Requests, !s.useRequested)
			if podRequest < value {
				podRequest = value
			}
		}

		// If Overhead is being utilized, add to the total requests for the pod
		if pod.Spec.Overhead != nil && s.enablePodOverhead {
			if quantity, found := pod.Spec.Overhead[resourceName]; found {
				podRequest += quantity.Value()
			}
		}
		podCount++
		fmt.Printf("pod.name: %+v, pod.APIVersion: %+v, pod.Namespace: %+v\n", pod.Name, pod.APIVersion, pod.Namespace)
	}

	fmt.Printf("podCount: %+v\n\n", podCount)
	return podRequest, nil
}

func (s *Score) getRequestForResource(resource v1.ResourceName, requests *v1.ResourceList, nonZero bool) int64 {
	if requests == nil {
		return 0
	}
	switch resource {
	case v1.ResourceCPU:
		// Override if un-set, but not if explicitly set to zero
		if _, found := (*requests)[v1.ResourceCPU]; !found && nonZero {
			return 100
		}
		return requests.Cpu().Value()
	case v1.ResourceMemory:
		// Override if un-set, but not if explicitly set to zero
		if _, found := (*requests)[v1.ResourceMemory]; !found && nonZero {
			return 200 * 1024 * 1024
		}
		return requests.Memory().Value()
	default:
		quantity, found := (*requests)[resource]
		if !found {
			return 0
		}
		return quantity.Value()
	}
}
