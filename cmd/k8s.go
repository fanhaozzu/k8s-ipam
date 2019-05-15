package main

import (
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/domeos/k8s-ipam/pkg/api/k8s.domeos.sohuno.com/v1alpha1"
	ipamclient "github.com/domeos/k8s-ipam/pkg/client/clientset/versioned"
	corev1 "k8s.io/api/core/v1"
	v1beta1 "k8s.io/api/apps/v1beta1"
	kubeerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

var ErrUpdateConflict = errors.New("failed to update, most likely due to resource version mismatch.  Did someone else update this?  Retry.")

type PodRetriever interface {
	GetPod(string, string) (*corev1.Pod, error)
	GetStatefulSet(string, string) (*v1beta1.StatefulSet, error)
}

type IPPoolManipulator interface {
	GetIPPool() (*v1alpha1.IPPool, error)
	UpdateIPPool(*v1alpha1.IPPool) error
}

type KubernetesAllocatorClient interface {
	PodRetriever
	IPPoolManipulator
}

type KubeClient struct {
	KubeConfig string
	IPPoolName string
}

func (k *KubeClient) client() (*kubernetes.Clientset, error) {
	conf, err := clientcmd.BuildConfigFromFlags("", k.KubeConfig)
	if err != nil {
		return nil, fmt.Errorf("unable to load kubeconfig from %s: %v", k.KubeConfig, err)
	}

	return kubernetes.NewForConfig(conf)
}

func (k *KubeClient) GetPod(namespace, podName string) (*corev1.Pod, error) {
	client, err := k.client()
	if err != nil {
		return nil, fmt.Errorf("error getting client: %v", err)
	}

	pod, err := client.CoreV1().Pods(namespace).Get(podName, metav1.GetOptions{})
	if err != nil && kubeerrors.IsNotFound(err) {
		return nil, nil
	}

	return pod, err
}

func (k *KubeClient) GetStatefulSet(namespace, stName string) (*v1beta1.StatefulSet, error) {
	client, err := k.client()
        if err != nil {
                return nil, fmt.Errorf("error getting client: %v", err)
        }

	st, err := client.AppsV1beta1().StatefulSets(namespace).Get(stName, metav1.GetOptions{})
	if err != nil && kubeerrors.IsNotFound(err) {
		return nil, nil
	}
	
	return st, err
}

func (k *KubeClient) GetIPPool() (*v1alpha1.IPPool, error) {
	conf, err := clientcmd.BuildConfigFromFlags("", k.KubeConfig)
	if err != nil {
		return nil, fmt.Errorf("unable to load kubeconfig from %s: %v", k.KubeConfig, err)
	}

	client, err := ipamclient.NewForConfig(conf)
	if err != nil {
		return nil, fmt.Errorf("unable to create client: %v", err)
	}

	return client.K8sV1alpha1().IPPools().Get(k.IPPoolName, metav1.GetOptions{})
}

func (k *KubeClient) UpdateIPPool(pool *v1alpha1.IPPool) error {
	conf, err := clientcmd.BuildConfigFromFlags("", k.KubeConfig)
	if err != nil {
		return fmt.Errorf("unable to load kubeconfig from %s: %v", k.KubeConfig, err)
	}

	client, err := ipamclient.NewForConfig(conf)
	if err != nil {
		return fmt.Errorf("unable to create client: %v", err)
	}

	_, err = client.K8sV1alpha1().IPPools().Update(pool)
	return err
}

type KubernetesAllocator struct {
	Client KubernetesAllocatorClient
}

func (a *KubernetesAllocator) Allocate(namespace, podName string) (ip net.IPNet, gateway net.IP, err error) {
	p, err := a.Client.GetIPPool()
	if err != nil {
		return ip, gateway, err
	}
	
	pod, err := a.Client.GetPod(namespace, podName)
        if err != nil {
		return ip, gateway, err
        }

	if pod == nil {
		return ip, gateway, fmt.Errorf("Pod is not exist")
	}

	if pod.Status.HostIP == "" {
		return ip, gateway, fmt.Errorf("Pod HostIP is not exist")
	}
	hostIP := net.ParseIP(pod.Status.HostIP)
	if hostIP == nil {
		return ip, gateway, fmt.Errorf("Pod HostIP is illegal: %v", pod.Status.HostIP)		
	}
	
	ipPoolSub := p.GetIPPoolSub(hostIP)	
	if ipPoolSub.Range == "NULL" {
		return ip, gateway, fmt.Errorf("IPPoolSub is null, can't find matched ipPool")
	}
	if err := ipPoolSub.Validate(); err != nil {
		return ip, gateway, fmt.Errorf("IPPoolSub is invalid.  Please check your configuration.  Error was: %v Got Spec: %v", err, ipPoolSub)
	}

	gateway = ipPoolSub.Gateway
	ip = net.IPNet{Mask: ipPoolSub.GetMask()}

	// * If an IP is already assigned to a pod with a matching name/namespace tuple, that ip is reassigned (any pod that's named the same will get the same IP when relaunched)
	if existingIP := p.GetExistingReservation(namespace, podName); existingIP != nil {
		ip.IP = *existingIP
		return ip, gateway, nil
	}
	// * Otherwise an IP is chosen randomly
	var allocatedIP *net.IP
	for allocatedIP == nil {
		candidateIP := ipPoolSub.RandomIP()
		if existingPodNS, existingPodName, found := p.GetPodForIP(candidateIP); found {
			// If the chosen IP is assigned, we check to see if the pod that has claimed it is still running.
			pod, err := a.Client.GetPod(existingPodNS, existingPodName)
			if err != nil {
				return ip, gateway, err
			}

			// * If the pod is running a new IP is chosen and the process is repeated until an ip is assigned.
			if pod != nil {
				continue
			}
			
			// * If the pod is no longer running, the ownerReferences(statefulSet) may be exist.
			podNames := strings.Split(existingPodName, "-")
			if podNames[len(podNames) - 2] == "st" {
				podNameIndex := strings.LastIndex(existingPodName, "-")
				stName := existingPodName[0:podNameIndex]
				st, err := a.Client.GetStatefulSet(existingPodNS, stName)
				if err != nil {
					return ip, gateway, err
				}
				if st == nil {
					p.FreeDynamicPodReservation(existingPodNS, existingPodName)
				} else {
					continue
				}
                        } else {
				p.FreeDynamicPodReservation(existingPodNS, existingPodName)
			}
		}

		allocatedIP = &candidateIP
		break
	}

	ip.IP = *allocatedIP

	if !ipPoolSub.RangeContains(*allocatedIP) {
		return ip, gateway, fmt.Errorf("somehow allocated ip not in network. %v", allocatedIP)
	}

	p.Reserve(namespace, podName, ip.IP)

	err = a.Client.UpdateIPPool(p)
	if err != nil && kubeerrors.IsConflict(err) {
		// update failed due to stale resourceversion
		return ip, gateway, ErrUpdateConflict
	}

	return ip, gateway, err
}

func (a *KubernetesAllocator) Free(namespace, podName string) error {
	p, err := a.Client.GetIPPool()
	if err != nil {
		return err
	}

	pod, err := a.Client.GetPod(namespace, podName)
	if err != nil {
		return err
	}

	if pod != nil {
		if len(pod.ObjectMeta.OwnerReferences) == 0 {
                	return errors.New("Pod ObjectMeta is invalid")
        	}       
        	for _, ownerReference := range pod.ObjectMeta.OwnerReferences {
                	if ownerReference.Kind == "StatefulSet" {
                        	return nil
                	}       
        	}	
	}

	p.FreeDynamicPodReservation(namespace, podName)

	err = a.Client.UpdateIPPool(p)
	if err != nil && kubeerrors.IsConflict(err) {
		// update failed due to stale resourceversion
		return ErrUpdateConflict
	}
	return err
}


