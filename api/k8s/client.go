package k8s

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
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

type VolumeMount struct {
	Name      string
	MountPath string
	PVCName   string // mutually exclusive with HostPath
	HostPath  string
}

type DeploymentOpts struct {
	Name        string
	Image       string
	Port        int
	Replicas    int
	Healthcheck string
	Env         []corev1.EnvVar
	Volumes     []VolumeMount
	SecretName  string // K8s Secret to inject as env vars via envFrom
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
			Replicas: ptr(int32(max(opts.Replicas, 1))),
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
						ImagePullPolicy: imagePullPolicy(opts.Image),
						Ports: []corev1.ContainerPort{{
							ContainerPort: int32(opts.Port),
						}},
						Env:     opts.Env,
						EnvFrom: envFromSecret(opts.SecretName),
					}},
				},
			},
		},
	}

	if len(opts.Volumes) > 0 {
		var volumes []corev1.Volume
		var mounts []corev1.VolumeMount
		for _, v := range opts.Volumes {
			vol := corev1.Volume{Name: v.Name}
			if v.HostPath != "" {
				vol.VolumeSource = corev1.VolumeSource{
					HostPath: &corev1.HostPathVolumeSource{
						Path: v.HostPath,
					},
				}
			} else {
				vol.VolumeSource = corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
						ClaimName: v.PVCName,
					},
				}
			}
			volumes = append(volumes, vol)
			mounts = append(mounts, corev1.VolumeMount{
				Name:      v.Name,
				MountPath: v.MountPath,
			})
		}
		dep.Spec.Template.Spec.Volumes = volumes
		dep.Spec.Template.Spec.Containers[0].VolumeMounts = mounts
	}

	if opts.Healthcheck != "" {
		handler := corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{
				Path: opts.Healthcheck,
				Port: intstr.FromInt32(int32(opts.Port)),
			},
		}
		dep.Spec.Template.Spec.Containers[0].StartupProbe = &corev1.Probe{
			ProbeHandler:     handler,
			PeriodSeconds:    5,
			FailureThreshold: 30, // 5s * 30 = 150s to start
		}
		dep.Spec.Template.Spec.Containers[0].ReadinessProbe = &corev1.Probe{
			ProbeHandler:  handler,
			PeriodSeconds: 10,
		}
		dep.Spec.Template.Spec.Containers[0].LivenessProbe = &corev1.Probe{
			ProbeHandler:     handler,
			PeriodSeconds:    30,
			FailureThreshold: 3,
		}
	}

	_, err := c.cs.AppsV1().Deployments(namespace).Create(ctx, dep, metav1.CreateOptions{})
	return err
}

func (c *Client) CreatePVC(ctx context.Context, namespace, name, size string, labels map[string]string) error {
	quantity, err := resource.ParseQuantity(size)
	if err != nil {
		return fmt.Errorf("invalid size %q: %w", size, err)
	}
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    labels,
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: quantity,
				},
			},
		},
	}
	_, err = c.cs.CoreV1().PersistentVolumeClaims(namespace).Create(ctx, pvc, metav1.CreateOptions{})
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

func (c *Client) SecretExists(ctx context.Context, namespace, name string) (bool, error) {
	_, err := c.cs.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (c *Client) DeleteDeployment(ctx context.Context, namespace, name string) error {
	err := c.cs.AppsV1().Deployments(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if k8serrors.IsNotFound(err) {
		return nil
	}
	return err
}

func (c *Client) DeleteService(ctx context.Context, namespace, name string) error {
	err := c.cs.CoreV1().Services(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if k8serrors.IsNotFound(err) {
		return nil
	}
	return err
}

func IsAlreadyExists(err error) bool {
	return k8serrors.IsAlreadyExists(err)
}

func IsNotFound(err error) bool {
	return k8serrors.IsNotFound(err)
}

func (c *Client) SetReplicas(ctx context.Context, namespace, name string, replicas int32) error {
	dep, err := c.cs.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	dep.Spec.Replicas = ptr(replicas)
	_, err = c.cs.AppsV1().Deployments(namespace).Update(ctx, dep, metav1.UpdateOptions{})
	return err
}

func envFromSecret(name string) []corev1.EnvFromSource {
	if name == "" {
		return nil
	}
	return []corev1.EnvFromSource{{
		SecretRef: &corev1.SecretEnvSource{
			LocalObjectReference: corev1.LocalObjectReference{Name: name},
			Optional:             ptr(true),
		},
	}}
}

func imagePullPolicy(image string) corev1.PullPolicy {
	// Images with a registry prefix (containing /) should be pulled
	// Local-only images (e.g. "myapp:latest") use PullNever
	if strings.Contains(image, "/") {
		return corev1.PullIfNotPresent
	}
	return corev1.PullNever
}

func ptr[T any](v T) *T { return &v }
