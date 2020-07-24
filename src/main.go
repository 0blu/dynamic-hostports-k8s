package main

import (
	"context"
	"errors"
	"flag"
	logLib "log"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

const servicePrefix = "dynamic-hostports-service"
const annotationPrefix = "dynamic-hostports.k8s"
const labelKey = "dynamic-hostports"

const managedByLabelKey = "app.kubernetes.io/managed-by"
const managedByLabelValue = annotationPrefix
const forPodLabelKey = "dynamic-hostports.k8s/for-pod"

var log = logLib.New(os.Stdout, "", 0)
var logErr = logLib.New(os.Stderr, "", 0)

// Will split a string of '8080.8082' to int32 array [8080, 8082]
func splitHostportStrings(portsString string) ([]int32, error) {
	splitted := strings.Split(portsString, ".")
	mapped := make([]int32, len(splitted))

	for i, val := range splitted {
		port, err := strconv.Atoi(val)
		if err != nil {
			return nil, err
		}
		if port <= 0 || port >= 65536 {
			return nil, errors.New("Port is not in valid range")
		}
		mapped[i] = int32(port)
	}

	return mapped, nil
}

func podPortToAnnotation(requestedPort int32) string {
	return annotationPrefix + "/" + strconv.Itoa(int(requestedPort))
}

func podPortToServiceName(pod *v1.Pod, requestedPort int32) string {
	return pod.Name + "-" + strconv.Itoa(int(requestedPort))
}

func createService(client *kubernetes.Clientset, pod *v1.Pod, requestedPort int32, cachedExternalIPs map[string]string) error {
	if pod.Annotations[podPortToAnnotation(requestedPort)] != "" {
		log.Printf("[%s] Pod already has service annotation for port %d. Skipping recreation.", pod.Name, requestedPort)
		return nil
	}
	log.Printf("[%s] Create service for port %d", pod.Name, requestedPort)

	serviceName := podPortToServiceName(pod, requestedPort)

	meta := metav1.ObjectMeta{
		Name:      serviceName,
		Namespace: pod.Namespace,
		Labels: map[string]string{
			managedByLabelKey: managedByLabelValue,
			forPodLabelKey:    pod.Name,
		},
	}

	_, err := client.CoreV1().Endpoints(pod.Namespace).Create(
		context.Background(),
		&v1.Endpoints{
			ObjectMeta: meta,
			Subsets: []v1.EndpointSubset{
				{
					Addresses: []v1.EndpointAddress{
						{
							IP: pod.Status.PodIP,
						},
					},
					Ports: []v1.EndpointPort{
						{
							Port: requestedPort,
							// Protocol: TODO: Detect the type of port of the port and then use TCP/UDP
						},
					},
				},
			},
		},
		metav1.CreateOptions{},
	)

	if err != nil {
		return err
	}

	serviceDef := v1.Service{
		ObjectMeta: meta,
		Spec: v1.ServiceSpec{
			Type: v1.ServiceTypeNodePort,
			Ports: []v1.ServicePort{
				{
					Port:       requestedPort,
					TargetPort: intstr.FromInt(int(requestedPort)),
					// Protocol: TODO: Detect the type of port of the port and then use TCP/UDP
				},
			},
		},
	}

	externalIp := getOrFetchExternalNodeIp(client, pod.Spec.NodeName, cachedExternalIPs)
	if externalIp != "" {
		serviceDef.Spec.ExternalIPs = []string{
			externalIp,
		}
	} else {
		log.Printf("[%s] Got no ip of node '%s' are you using minikube? The service will exposed over all nodes.", pod.Name, pod.Spec.NodeName)
	}

	newService, err := client.CoreV1().Services(pod.Namespace).Create(
		context.Background(),
		&serviceDef,
		metav1.CreateOptions{},
	)
	if err != nil {
		return err
	}

	err = addPodPortAnnotation(client, pod, requestedPort, newService.Spec.Ports[0].NodePort)
	if err != nil {
		return err
	}

	return nil
}

func getOrFetchExternalNodeIp(client *kubernetes.Clientset, nodeName string, cachedExternalIPs map[string]string) string {
	ip := ""
	knowsIP := false
	if ip, knowsIP = cachedExternalIPs[nodeName]; !knowsIP {
		node, err := client.CoreV1().Nodes().Get(context.Background(), nodeName, metav1.GetOptions{})
		if err != nil {
			log.Printf("Got an error while fetching external ip of node '%s'. %s", nodeName, err)
			return ""
		}
		for _, addr := range node.Status.Addresses {
			if addr.Type == v1.NodeExternalIP {
				ip = addr.Address
				log.Printf("Caching ip of node '%s' => %s", nodeName, ip)
				cachedExternalIPs[nodeName] = ip
				break
			}
		}
	}

	return ip
}

func addPodPortAnnotation(client *kubernetes.Clientset, pod *v1.Pod, requestedPort int32, dynamicPort int32) error {
	// This is kinda hacky, since we need to ensure that .metadata.annotations is available
	serializedJson := []byte(`{
	"kind": "Pod",
	"apiVersion": "v1",
	"metadata": {
		"annotations": {
			"` + annotationPrefix + `/` + strconv.Itoa(int(requestedPort)) + `": "` + strconv.Itoa(int(dynamicPort)) + `"
		}
	}
}`)

	_, err := client.CoreV1().Pods(pod.Namespace).Patch(
		context.Background(),
		pod.Name,
		types.MergePatchType,
		serializedJson,
		metav1.PatchOptions{},
	)
	if err != nil {
		logErr.Printf("[%s] Adding annotation %d=>%d failed %s", pod.Name, requestedPort, dynamicPort, err)
	}

	return err
}

func deleteService(client *kubernetes.Clientset, namespace string, serviceName string) error {
	return client.CoreV1().Services(namespace).Delete(context.Background(), serviceName, metav1.DeleteOptions{})
}

func deletePodServices(client *kubernetes.Clientset, pod *v1.Pod) error {
	requestedPorts, err := splitHostportStrings(pod.Labels[labelKey])
	if err != nil {
		return err
	}

	for _, requestedPort := range requestedPorts {
		log.Printf("[%s] Deleting service for port %d.", pod.Name, requestedPort)
		err := deleteService(client, pod.Namespace, podPortToServiceName(pod, requestedPort))
		if err != nil {
			return err
		}
	}

	return nil
}

func handlePodEvent(client *kubernetes.Clientset, eventType watch.EventType, pod *v1.Pod, handledPods map[string]bool, cachedExternalIPs map[string]string) error {
	namespacedPodName := pod.Namespace + "/" + pod.Name // Prevent multiple attempts of creating a service
	if eventType == watch.Deleted {
		delete(handledPods, namespacedPodName)
		err := deletePodServices(client, pod)
		if err != nil {
			return err
		}
	} else {
		if handledPods[namespacedPodName] {
			log.Printf("[%s] Ignoring pod because it was already handled.", pod.Name)
			return nil
		}

		if pod.Status.PodIP == "" {
			log.Printf("[%s] Ignoring pod because it does not have an ip.", pod.Name)
			return nil
		}

		if pod.Status.Phase != v1.PodRunning {
			log.Printf("[%s] Ignoring pod because it is not running.", pod.Name)
			return nil
		}

		requestedPorts, err := splitHostportStrings(pod.Labels[labelKey])
		if err != nil {
			return err
		}

		handledPods[namespacedPodName] = true

		for _, requestedPort := range requestedPorts {
			err := createService(client, pod, requestedPort, cachedExternalIPs)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

func podManagerRoutine(client *kubernetes.Clientset, namespace string) {
	cachedExternalIPs := make(map[string]string)
	handledPods := make(map[string]bool)

	timeout := int64(60 * 60 * 24) // 24 hours
	log.Print("Watching pods")
	for {
		watcher, err := client.CoreV1().Pods(namespace).Watch(context.Background(), metav1.ListOptions{
			LabelSelector:  labelKey,
			TimeoutSeconds: &timeout,
		})
		if err != nil {
			logErr.Panicf("Error while create watch for pods %s", err)
		}
		eventChannel := watcher.ResultChan()
		for event := range eventChannel {
			pod, ok := event.Object.(*v1.Pod)
			if !ok {
				logErr.Panic("Unexpected watch object")
			}
			err := handlePodEvent(client, event.Type, pod, handledPods, cachedExternalIPs)
			if err != nil {
				logErr.Printf("[%s] Failed to handle event %s", pod.Name, err)
			}
		}
		log.Print("Restart loop")
	}
}

func deleteStaleServices(client *kubernetes.Clientset, namespace string) error {
	pods, err := client.CoreV1().Pods(namespace).List(context.Background(), metav1.ListOptions{
		LabelSelector: labelKey,
	})

	services, err := client.CoreV1().Services(namespace).List(context.Background(), metav1.ListOptions{
		LabelSelector: managedByLabelKey + "=" + managedByLabelValue,
	})
	if err != nil {
		return err
	}

	for _, service := range services.Items {
		forPod := service.Labels[forPodLabelKey]

		foundPod := false
		for _, pod := range pods.Items {
			if pod.Name == forPod && pod.Namespace == service.Namespace {
				foundPod = true
				break
			}
		}
		if !foundPod {
			log.Printf("Delete stale service '%s'", service.Name)
			localErr := deleteService(client, service.Namespace, service.Name)
			if localErr != nil {
				logErr.Printf("Failed to delete service %s", localErr)
			}
		}
	}

	return nil
}

func serviceManagerRoutine(client *kubernetes.Clientset, namespace string) {
	err := deleteStaleServices(client, namespace)
	if err != nil {
		logErr.Panicf("Error while deleting stale services %s", err)
	}
}

// ----------------- Start stuff -----------------

func homeDir() string {
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	return os.Getenv("USERPROFILE") // Windows
}

func getBestConfig() (*rest.Config, error) {
	var config *rest.Config
	var err error

	config, err = rest.InClusterConfig()
	if err == nil {
		return config, nil
	}
	if err != rest.ErrNotInCluster {
		return nil, err
	}

	// We have to fall back to the local kube config if we are not in a cluster
	var kubeconfig *string
	if home := homeDir(); home != "" {
		kubeconfig = flag.String("kubeconfig", filepath.Join(home, ".kube", "config"), "(optional) absolute path to the kubeconfig file")
	} else {
		kubeconfig = flag.String("kubeconfig", "", "absolute path to the kubeconfig file")
	}
	namespace := *flag.String("namespace", "", "The namespace that this should apply to (can also be set via KUBERNETES_NAMESPACE environment variable)")
	if namespace == "" {
		namespace = os.Getenv("KUBERNETES_NAMESPACE")
	}
	os.Setenv("KUBERNETES_NAMESPACE", namespace)
	flag.Parse()

	config, err = clientcmd.BuildConfigFromFlags("", *kubeconfig)
	if err != nil {
		return nil, err
	}
	return config, nil
}

func createClientset() (*kubernetes.Clientset, error) {
	config, err := getBestConfig()
	if err != nil {
		return nil, err
	}

	return kubernetes.NewForConfig(config)
}

func main() {
	log.Print("Starting...")

	client, err := createClientset()
	if err != nil {
		panic(err.Error())
	}
	namespace := os.Getenv("KUBERNETES_NAMESPACE")

	serviceManagerRoutine(client, namespace)
	podManagerRoutine(client, namespace)
}
