package kubernetes

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/creydr/ai-coworker/internal/sandbox"
)

func TestBuildEnvVars(t *testing.T) {
	tests := []struct {
		name string
		in   map[string]string
		want int
	}{
		{"nil", nil, 0},
		{"empty", map[string]string{}, 0},
		{"single", map[string]string{"KEY": "val"}, 1},
		{"multiple", map[string]string{"A": "1", "B": "2", "C": "3"}, 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := buildEnvVars(tt.in)
			if len(result) != tt.want {
				t.Errorf("len(buildEnvVars) = %d, want %d", len(result), tt.want)
			}
			for _, ev := range result {
				if v, ok := tt.in[ev.Name]; !ok || v != ev.Value {
					t.Errorf("env var %s=%q not found in input", ev.Name, ev.Value)
				}
			}
		})
	}
}

func TestBuildResources(t *testing.T) {
	tests := []struct {
		name     string
		cpu      string
		mem      string
		wantCPU  string
		wantMem  string
		wantNone bool
	}{
		{"empty", "", "", "", "", true},
		{"cpu only", "2", "", "2", "", false},
		{"memory only", "", "2Gi", "", "2Gi", false},
		{"both", "500m", "512Mi", "500m", "512Mi", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := buildResources(tt.cpu, tt.mem)
			if tt.wantNone {
				if result.Limits != nil || result.Requests != nil {
					t.Error("expected empty ResourceRequirements")
				}
				return
			}
			if tt.wantCPU != "" {
				got := result.Limits[corev1.ResourceCPU]
				want := resource.MustParse(tt.wantCPU)
				if got.Cmp(want) != 0 {
					t.Errorf("CPU limit = %s, want %s", got.String(), want.String())
				}
				gotReq := result.Requests[corev1.ResourceCPU]
				if gotReq.Cmp(want) != 0 {
					t.Errorf("CPU request = %s, want %s (same as limit)", gotReq.String(), want.String())
				}
			}
			if tt.wantMem != "" {
				got := result.Limits[corev1.ResourceMemory]
				want := resource.MustParse(tt.wantMem)
				if got.Cmp(want) != 0 {
					t.Errorf("Memory limit = %s, want %s", got.String(), want.String())
				}
			}
		})
	}
}

func TestBuildConfigMap(t *testing.T) {
	cm := buildConfigMap("sandbox-abc123", "test-ns", "do something")

	if cm.Name != "sandbox-abc123" {
		t.Errorf("Name = %q, want %q", cm.Name, "sandbox-abc123")
	}
	if cm.Namespace != "test-ns" {
		t.Errorf("Namespace = %q, want %q", cm.Namespace, "test-ns")
	}
	if cm.Labels["app.kubernetes.io/managed-by"] != "ai-coworker" {
		t.Errorf("managed-by label = %q, want %q", cm.Labels["app.kubernetes.io/managed-by"], "ai-coworker")
	}
	if cm.Data["prompt.txt"] != "do something" {
		t.Errorf("Data[prompt.txt] = %q, want %q", cm.Data["prompt.txt"], "do something")
	}
}

func TestBuildJob(t *testing.T) {
	req := sandbox.ExecRequest{
		Image:   "quay.io/test/sandbox:latest",
		EnvVars: map[string]string{"KEY": "val"},
	}
	resources := buildResources("1", "1Gi")
	job := buildJob("sandbox-abc123", "test-ns", "my-sa", req, resources)

	if job.Name != "sandbox-abc123" {
		t.Errorf("Name = %q, want %q", job.Name, "sandbox-abc123")
	}
	if job.Namespace != "test-ns" {
		t.Errorf("Namespace = %q, want %q", job.Namespace, "test-ns")
	}
	if *job.Spec.BackoffLimit != 0 {
		t.Errorf("BackoffLimit = %d, want 0", *job.Spec.BackoffLimit)
	}
	if *job.Spec.TTLSecondsAfterFinished != 3600 {
		t.Errorf("TTLSecondsAfterFinished = %d, want 3600", *job.Spec.TTLSecondsAfterFinished)
	}

	pod := job.Spec.Template.Spec
	if pod.RestartPolicy != corev1.RestartPolicyNever {
		t.Errorf("RestartPolicy = %q, want %q", pod.RestartPolicy, corev1.RestartPolicyNever)
	}
	if pod.ServiceAccountName != "my-sa" {
		t.Errorf("ServiceAccountName = %q, want %q", pod.ServiceAccountName, "my-sa")
	}
	if len(pod.Containers) != 1 {
		t.Fatalf("len(Containers) = %d, want 1", len(pod.Containers))
	}

	c := pod.Containers[0]
	if c.Image != "quay.io/test/sandbox:latest" {
		t.Errorf("Image = %q, want %q", c.Image, "quay.io/test/sandbox:latest")
	}
	if len(c.VolumeMounts) != 1 || c.VolumeMounts[0].MountPath != "/tmp/prompt.txt" {
		t.Errorf("VolumeMounts not configured correctly")
	}
	if len(pod.Volumes) != 1 || pod.Volumes[0].ConfigMap.Name != "sandbox-abc123" {
		t.Errorf("Volumes not configured correctly")
	}
}
