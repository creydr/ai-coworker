package kubernetes

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"

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
			result, err := buildResources(tt.cpu, tt.mem)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
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

func TestBuildResources_InvalidInput(t *testing.T) {
	tests := []struct {
		name    string
		cpu     string
		mem     string
		wantMsg string
	}{
		{"invalid cpu", "not-a-quantity", "", "invalid CPU limit"},
		{"invalid memory", "", "not-a-quantity", "invalid memory limit"},
		{"invalid cpu with valid memory", "bad", "512Mi", "invalid CPU limit"},
		{"valid cpu with invalid memory", "500m", "bad", "invalid memory limit"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := buildResources(tt.cpu, tt.mem)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("error %q should contain %q", err.Error(), tt.wantMsg)
			}
		})
	}
}

func TestBuildPromptSecret(t *testing.T) {
	s := buildPromptSecret("sandbox-abc123", "test-ns", "do something")

	if s.Name != "sandbox-abc123" {
		t.Errorf("Name = %q, want %q", s.Name, "sandbox-abc123")
	}
	if s.Namespace != "test-ns" {
		t.Errorf("Namespace = %q, want %q", s.Namespace, "test-ns")
	}
	if s.Labels["app.kubernetes.io/managed-by"] != "ai-coworker" {
		t.Errorf("managed-by label = %q, want %q", s.Labels["app.kubernetes.io/managed-by"], "ai-coworker")
	}
	if string(s.Data["prompt.txt"]) != "do something" {
		t.Errorf("Data[prompt.txt] = %q, want %q", string(s.Data["prompt.txt"]), "do something")
	}
}

func TestBuildJob(t *testing.T) {
	req := sandbox.ExecRequest{
		Image:   "quay.io/test/sandbox:latest",
		EnvVars: map[string]string{"KEY": "val"},
	}
	resources, err := buildResources("1", "1Gi")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
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
	if len(pod.Volumes) != 1 || pod.Volumes[0].Secret == nil || pod.Volumes[0].Secret.SecretName != "sandbox-abc123" {
		t.Errorf("Volumes not configured correctly")
	}

	if c.SecurityContext == nil {
		t.Fatal("SecurityContext is nil")
	}
	if c.SecurityContext.RunAsNonRoot == nil || !*c.SecurityContext.RunAsNonRoot {
		t.Error("RunAsNonRoot should be true")
	}
	if c.SecurityContext.AllowPrivilegeEscalation == nil || *c.SecurityContext.AllowPrivilegeEscalation {
		t.Error("AllowPrivilegeEscalation should be false")
	}
	if c.SecurityContext.Capabilities == nil || len(c.SecurityContext.Capabilities.Drop) != 1 || c.SecurityContext.Capabilities.Drop[0] != "ALL" {
		t.Errorf("Capabilities.Drop = %v, want [ALL]", c.SecurityContext.Capabilities)
	}
}

func TestBuildJobWithSkillImages(t *testing.T) {
	req := sandbox.ExecRequest{
		Image:       "quay.io/test/sandbox:latest",
		EnvVars:     map[string]string{"KEY": "val"},
		SkillImages: []string{"quay.io/org/skills-a:latest", "ghcr.io/org/skills-b:v1"},
	}
	resources, err := buildResources("1", "1Gi")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	job := buildJob("sandbox-abc123", "test-ns", "my-sa", req, resources)

	pod := job.Spec.Template.Spec
	c := pod.Containers[0]

	// 1 prompt + 2 skill mounts
	if len(c.VolumeMounts) != 3 {
		t.Fatalf("len(VolumeMounts) = %d, want 3", len(c.VolumeMounts))
	}
	if c.VolumeMounts[1].MountPath != "/opt/skills-0" || !c.VolumeMounts[1].ReadOnly {
		t.Errorf("skill-0 mount = %+v, want /opt/skills-0 read-only", c.VolumeMounts[1])
	}
	if c.VolumeMounts[2].MountPath != "/opt/skills-1" || !c.VolumeMounts[2].ReadOnly {
		t.Errorf("skill-1 mount = %+v, want /opt/skills-1 read-only", c.VolumeMounts[2])
	}

	// 1 prompt volume + 2 skill image volumes
	if len(pod.Volumes) != 3 {
		t.Fatalf("len(Volumes) = %d, want 3", len(pod.Volumes))
	}
	if pod.Volumes[1].Image == nil || pod.Volumes[1].Image.Reference != "quay.io/org/skills-a:latest" {
		t.Errorf("skill-0 volume image = %+v, want quay.io/org/skills-a:latest", pod.Volumes[1])
	}
	if pod.Volumes[1].Image.PullPolicy != corev1.PullIfNotPresent {
		t.Errorf("skill-0 pullPolicy = %q, want IfNotPresent", pod.Volumes[1].Image.PullPolicy)
	}
	if pod.Volumes[2].Image == nil || pod.Volumes[2].Image.Reference != "ghcr.io/org/skills-b:v1" {
		t.Errorf("skill-1 volume image = %+v, want ghcr.io/org/skills-b:v1", pod.Volumes[2])
	}
}

func TestBuildJobWithoutSkillImages(t *testing.T) {
	req := sandbox.ExecRequest{
		Image:   "quay.io/test/sandbox:latest",
		EnvVars: map[string]string{"KEY": "val"},
	}
	resources, err := buildResources("1", "1Gi")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	job := buildJob("sandbox-abc123", "test-ns", "my-sa", req, resources)

	pod := job.Spec.Template.Spec
	c := pod.Containers[0]

	if len(c.VolumeMounts) != 1 {
		t.Errorf("len(VolumeMounts) = %d, want 1 (prompt only)", len(c.VolumeMounts))
	}
	if len(pod.Volumes) != 1 {
		t.Errorf("len(Volumes) = %d, want 1 (prompt only)", len(pod.Volumes))
	}
}

func TestGetPodExitCode(t *testing.T) {
	tests := []struct {
		name         string
		pods         []runtime.Object
		wantExitCode int32
		wantReason   string
	}{
		{
			name:         "no pods",
			pods:         nil,
			wantExitCode: 1,
			wantReason:   "",
		},
		{
			name: "terminated with OOMKilled",
			pods: []runtime.Object{
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "sandbox-abc-xyz",
						Namespace: "test-ns",
						Labels:    map[string]string{"job-name": "sandbox-abc"},
					},
					Status: corev1.PodStatus{
						ContainerStatuses: []corev1.ContainerStatus{{
							State: corev1.ContainerState{
								Terminated: &corev1.ContainerStateTerminated{
									ExitCode: 137,
									Reason:   "OOMKilled",
								},
							},
						}},
					},
				},
			},
			wantExitCode: 137,
			wantReason:   "OOMKilled",
		},
		{
			name: "terminated with exit code 2",
			pods: []runtime.Object{
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "sandbox-abc-xyz",
						Namespace: "test-ns",
						Labels:    map[string]string{"job-name": "sandbox-abc"},
					},
					Status: corev1.PodStatus{
						ContainerStatuses: []corev1.ContainerStatus{{
							State: corev1.ContainerState{
								Terminated: &corev1.ContainerStateTerminated{
									ExitCode: 2,
									Reason:   "Error",
								},
							},
						}},
					},
				},
			},
			wantExitCode: 2,
			wantReason:   "Error",
		},
		{
			name: "no terminated state",
			pods: []runtime.Object{
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "sandbox-abc-xyz",
						Namespace: "test-ns",
						Labels:    map[string]string{"job-name": "sandbox-abc"},
					},
					Status: corev1.PodStatus{
						ContainerStatuses: []corev1.ContainerStatus{{
							State: corev1.ContainerState{},
						}},
					},
				},
			},
			wantExitCode: 1,
			wantReason:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cs := fake.NewSimpleClientset(tt.pods...)
			r := NewWithClient(cs, "test-ns", "")
			exitCode, reason := r.getPodExitCode(t.Context(), "sandbox-abc")
			if exitCode != tt.wantExitCode {
				t.Errorf("exitCode = %d, want %d", exitCode, tt.wantExitCode)
			}
			if reason != tt.wantReason {
				t.Errorf("reason = %q, want %q", reason, tt.wantReason)
			}
		})
	}
}
