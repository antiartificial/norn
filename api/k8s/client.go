package k8s

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"os"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

type Client struct {
	cs *kubernetes.Clientset
}

func NewClient() (*Client, error) {
	config, err := rest.InClusterConfig()
	if err != nil {
		kubeconfig := filepath.Join(os.Getenv("HOME"), ".kube", "config")
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			return nil, fmt.Errorf("k8s config: %w", err)
		}
	}
	cs, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("k8s clientset: %w", err)
	}
	return &Client{cs: cs}, nil
}

func (c *Client) ListDeployments(ctx context.Context, namespace string) ([]DeploymentInfo, error) {
	deps, err := c.cs.AppsV1().Deployments(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "managed-by=norn",
	})
	if err != nil {
		return nil, err
	}

	var result []DeploymentInfo
	for _, d := range deps.Items {
		info := DeploymentInfo{
			Name:      d.Name,
			Namespace: d.Namespace,
			Replicas:  int(*d.Spec.Replicas),
			Ready:     int(d.Status.ReadyReplicas),
			Image:     d.Spec.Template.Spec.Containers[0].Image,
		}
		result = append(result, info)
	}
	return result, nil
}

func (c *Client) GetPods(ctx context.Context, namespace, appLabel string) ([]corev1.Pod, error) {
	pods, err := c.cs.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("app=%s", appLabel),
	})
	if err != nil {
		return nil, err
	}
	return pods.Items, nil
}

func (c *Client) StreamLogs(ctx context.Context, namespace, podName string, follow bool) (io.ReadCloser, error) {
	req := c.cs.CoreV1().Pods(namespace).GetLogs(podName, &corev1.PodLogOptions{
		Follow:     follow,
		TailLines:  ptr(int64(200)),
		Timestamps: true,
	})
	return req.Stream(ctx)
}

func (c *Client) RestartDeployment(ctx context.Context, namespace, name string) error {
	dep, err := c.cs.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if dep.Spec.Template.Annotations == nil {
		dep.Spec.Template.Annotations = make(map[string]string)
	}
	dep.Spec.Template.Annotations["norn/restartedAt"] = metav1.Now().Format("2006-01-02T15:04:05Z")
	_, err = c.cs.AppsV1().Deployments(namespace).Update(ctx, dep, metav1.UpdateOptions{})
	return err
}

func (c *Client) SetImage(ctx context.Context, namespace, deployName, container, image string) error {
	dep, err := c.cs.AppsV1().Deployments(namespace).Get(ctx, deployName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	for i := range dep.Spec.Template.Spec.Containers {
		if dep.Spec.Template.Spec.Containers[i].Name == container {
			dep.Spec.Template.Spec.Containers[i].Image = image
		}
	}
	_, err = c.cs.AppsV1().Deployments(namespace).Update(ctx, dep, metav1.UpdateOptions{})
	return err
}

type DeploymentInfo struct {
	Name      string
	Namespace string
	Replicas  int
	Ready     int
	Image     string
}

func ptr[T any](v T) *T { return &v }
