package kubernetes

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/google/uuid"

	"github.com/creydr/ai-coworker/internal/sandbox"
)

var _ sandbox.Runtime = (*Runtime)(nil)

type Runtime struct {
	clientset      kubernetes.Interface
	namespace      string
	serviceAccount string
}

func New(namespace, serviceAccount string) (*Runtime, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to create in-cluster config: %w", err)
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create kubernetes client: %w", err)
	}
	return &Runtime{clientset: cs, namespace: namespace, serviceAccount: serviceAccount}, nil
}

func NewWithClient(cs kubernetes.Interface, namespace, serviceAccount string) *Runtime {
	return &Runtime{clientset: cs, namespace: namespace, serviceAccount: serviceAccount}
}

func (r *Runtime) Close() error {
	return nil
}

func (r *Runtime) Exec(ctx context.Context, req sandbox.ExecRequest) (*sandbox.ExecResult, error) {
	name := "sandbox-" + uuid.New().String()[:8]

	sandbox.PrepareEnvVars(&req)

	if req.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(req.Timeout)*time.Second)
		defer cancel()
	}

	cm := buildConfigMap(name, r.namespace, req.Prompt)
	if _, err := r.clientset.CoreV1().ConfigMaps(r.namespace).Create(ctx, cm, metav1.CreateOptions{}); err != nil {
		return nil, fmt.Errorf("failed to create prompt configmap: %w", err)
	}

	resources := buildResources(req.CPULimit, req.MemLimit)
	job := buildJob(name, r.namespace, r.serviceAccount, req, resources)
	if _, err := r.clientset.BatchV1().Jobs(r.namespace).Create(ctx, job, metav1.CreateOptions{}); err != nil {
		r.cleanup(name)
		return nil, fmt.Errorf("failed to create sandbox job: %w", err)
	}
	slog.Info("sandbox job created", "job", name, "namespace", r.namespace)

	defer r.cleanup(name)

	exitCode, jobErr, err := r.waitForJob(ctx, name)
	if err != nil {
		return nil, err
	}

	if jobErr != "" {
		return &sandbox.ExecResult{
			ExitCode: int(exitCode),
			Error:    jobErr,
		}, nil
	}

	output, err := r.readPodLogs(ctx, name)
	if err != nil {
		return nil, err
	}

	return &sandbox.ExecResult{
		Output:   output,
		ExitCode: int(exitCode),
	}, nil
}

func (r *Runtime) waitForJob(ctx context.Context, name string) (int32, string, error) {
	watcher, err := r.clientset.BatchV1().Jobs(r.namespace).Watch(ctx, metav1.ListOptions{
		FieldSelector: "metadata.name=" + name,
	})
	if err != nil {
		return 0, "", fmt.Errorf("failed to watch job: %w", err)
	}
	defer watcher.Stop()

	for event := range watcher.ResultChan() {
		if event.Type == watch.Error {
			return 0, "", fmt.Errorf("watch error for job %s", name)
		}
		job, ok := event.Object.(*batchv1.Job)
		if !ok {
			continue
		}
		if job.Status.Succeeded > 0 {
			return 0, "", nil
		}
		if job.Status.Failed > 0 {
			msg := ""
			if len(job.Status.Conditions) > 0 {
				msg = job.Status.Conditions[0].Message
			}
			return 1, msg, nil
		}
	}

	return 0, "", fmt.Errorf("job watch closed unexpectedly for %s", name)
}

func (r *Runtime) readPodLogs(ctx context.Context, jobName string) (string, error) {
	pods, err := r.clientset.CoreV1().Pods(r.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "job-name=" + jobName,
	})
	if err != nil {
		return "", fmt.Errorf("failed to list pods for job %s: %w", jobName, err)
	}
	if len(pods.Items) == 0 {
		return "", fmt.Errorf("no pods found for job %s", jobName)
	}

	logStream, err := r.clientset.CoreV1().Pods(r.namespace).GetLogs(pods.Items[0].Name, &corev1.PodLogOptions{}).Stream(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to stream pod logs: %w", err)
	}
	defer logStream.Close()

	data, err := io.ReadAll(logStream)
	if err != nil {
		return "", fmt.Errorf("failed to read pod logs: %w", err)
	}
	return string(data), nil
}

func (r *Runtime) cleanup(name string) {
	propagation := metav1.DeletePropagationBackground
	_ = r.clientset.BatchV1().Jobs(r.namespace).Delete(context.Background(), name, metav1.DeleteOptions{
		PropagationPolicy: &propagation,
	})
	_ = r.clientset.CoreV1().ConfigMaps(r.namespace).Delete(context.Background(), name, metav1.DeleteOptions{})
	slog.Info("sandbox job cleaned up", "job", name)
}

func buildConfigMap(name, namespace, prompt string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    map[string]string{"app.kubernetes.io/managed-by": "ai-coworker"},
		},
		Data: map[string]string{
			"prompt.txt": prompt,
		},
	}
}

func buildJob(name, namespace, serviceAccount string, req sandbox.ExecRequest, resources corev1.ResourceRequirements) *batchv1.Job {
	backoffLimit := int32(0)
	ttl := int32(3600)

	volumeMounts := []corev1.VolumeMount{{
		Name:      "prompt",
		MountPath: "/tmp/prompt.txt",
		SubPath:   "prompt.txt",
		ReadOnly:  true,
	}}

	volumes := []corev1.Volume{{
		Name: "prompt",
		VolumeSource: corev1.VolumeSource{
			ConfigMap: &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: name,
				},
			},
		},
	}}

	for i, img := range req.SkillImages {
		volName := fmt.Sprintf("skill-%d", i)
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      volName,
			MountPath: fmt.Sprintf("/opt/skills-%d", i),
			ReadOnly:  true,
		})
		volumes = append(volumes, corev1.Volume{
			Name: volName,
			VolumeSource: corev1.VolumeSource{
				Image: &corev1.ImageVolumeSource{
					Reference:  img,
					PullPolicy: corev1.PullIfNotPresent,
				},
			},
		})
	}

	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    map[string]string{"app.kubernetes.io/managed-by": "ai-coworker"},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            &backoffLimit,
			TTLSecondsAfterFinished: &ttl,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					RestartPolicy:      corev1.RestartPolicyNever,
					ServiceAccountName: serviceAccount,
					Containers: []corev1.Container{{
						Name:         "sandbox",
						Image:        req.Image,
						Env:          buildEnvVars(req.EnvVars),
						Resources:    resources,
						VolumeMounts: volumeMounts,
						SecurityContext: &corev1.SecurityContext{
							RunAsNonRoot:             boolPtr(true),
							AllowPrivilegeEscalation: boolPtr(false),
							Capabilities: &corev1.Capabilities{
								Drop: []corev1.Capability{"ALL"},
							},
						},
					}},
					Volumes: volumes,
				},
			},
		},
	}
}

func boolPtr(b bool) *bool { return &b }

func buildEnvVars(envVars map[string]string) []corev1.EnvVar {
	vars := make([]corev1.EnvVar, 0, len(envVars))
	for k, v := range envVars {
		vars = append(vars, corev1.EnvVar{Name: k, Value: v})
	}
	return vars
}

func buildResources(cpuLimit, memLimit string) corev1.ResourceRequirements {
	limits := corev1.ResourceList{}
	if cpuLimit != "" {
		limits[corev1.ResourceCPU] = resource.MustParse(cpuLimit)
	}
	if memLimit != "" {
		limits[corev1.ResourceMemory] = resource.MustParse(memLimit)
	}
	if len(limits) == 0 {
		return corev1.ResourceRequirements{}
	}
	return corev1.ResourceRequirements{
		Limits:   limits,
		Requests: limits,
	}
}
