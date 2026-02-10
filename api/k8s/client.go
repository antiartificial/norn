package k8s

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
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

type DeploymentOpts struct {
	Name        string
	Image       string
	Port        int
	Healthcheck string
	Env         []corev1.EnvVar
}

func (c *Client) GetDeployment(ctx context.Context, namespace, name string) (*appsv1.Deployment, error) {
	return c.cs.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
}

func (c *Client) CreateDeployment(ctx context.Context, namespace string, opts DeploymentOpts) error {
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      opts.Name,
			Namespace: namespace,
			Labels:    map[string]string{"managed-by": "norn", "app": opts.Name},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr(int32(1)),
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": opts.Name},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app": opts.Name, "managed-by": "norn"},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:            opts.Name,
						Image:           opts.Image,
						ImagePullPolicy: corev1.PullNever,
						Ports: []corev1.ContainerPort{{
							ContainerPort: int32(opts.Port),
						}},
						Env: opts.Env,
					}},
				},
			},
		},
	}

	if opts.Healthcheck != "" {
		probe := &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path: opts.Healthcheck,
					Port: intstr.FromInt32(int32(opts.Port)),
				},
			},
			InitialDelaySeconds: 5,
			PeriodSeconds:       10,
		}
		dep.Spec.Template.Spec.Containers[0].ReadinessProbe = probe
		dep.Spec.Template.Spec.Containers[0].LivenessProbe = &corev1.Probe{
			ProbeHandler: probe.ProbeHandler,
			InitialDelaySeconds: 10,
			PeriodSeconds:       30,
		}
	}

	_, err := c.cs.AppsV1().Deployments(namespace).Create(ctx, dep, metav1.CreateOptions{})
	return err
}

func (c *Client) CreateService(ctx context.Context, namespace, appName, serviceName string, targetPort int) error {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      serviceName,
			Namespace: namespace,
			Labels:    map[string]string{"managed-by": "norn", "app": appName},
		},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": appName},
			Type:     corev1.ServiceTypeClusterIP,
			Ports: []corev1.ServicePort{{
				Port:       80,
				TargetPort: intstr.FromInt32(int32(targetPort)),
				Protocol:   corev1.ProtocolTCP,
			}},
		},
	}
	_, err := c.cs.CoreV1().Services(namespace).Create(ctx, svc, metav1.CreateOptions{})
	return err
}

func (c *Client) PatchConfigMap(ctx context.Context, namespace, name, dataKey string, patchFn func(string) (string, error)) error {
	cm, err := c.cs.CoreV1().ConfigMaps(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get configmap %s: %w", name, err)
	}
	original := cm.Data[dataKey]
	patched, err := patchFn(original)
	if err != nil {
		return fmt.Errorf("patch configmap data: %w", err)
	}
	cm.Data[dataKey] = patched
	_, err = c.cs.CoreV1().ConfigMaps(namespace).Update(ctx, cm, metav1.UpdateOptions{})
	return err
}

func ptr[T any](v T) *T { return &v }
