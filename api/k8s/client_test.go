package k8s

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestImagePullPolicy(t *testing.T) {
	tests := []struct {
		image string
		want  corev1.PullPolicy
	}{
		{"myapp:latest", corev1.PullNever},
		{"myapp:v1.2.3", corev1.PullNever},
		{"ghcr.io/user/myapp:v1", corev1.PullIfNotPresent},
		{"registry.example.com/app:tag", corev1.PullIfNotPresent},
		{"docker.io/library/nginx:latest", corev1.PullIfNotPresent},
	}

	for _, tt := range tests {
		t.Run(tt.image, func(t *testing.T) {
			got := imagePullPolicy(tt.image)
			if got != tt.want {
				t.Errorf("imagePullPolicy(%q) = %v, want %v", tt.image, got, tt.want)
			}
		})
	}
}
