package docker

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"

	"github.com/creydr/ai-coworker/internal/sandbox"
)

var _ sandbox.Runtime = (*Runtime)(nil)

// Runtime implements the sandbox runtime interface using Docker containers
type Runtime struct {
	client *client.Client
}

// New creates a new Docker runtime using the environment's Docker configuration
func New() (*Runtime, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("failed to create docker client: %w", err)
	}
	return &Runtime{client: cli}, nil
}

func (r *Runtime) Close() error {
	if r.client != nil {
		return r.client.Close()
	}
	return nil
}

func (r *Runtime) Exec(ctx context.Context, req sandbox.ExecRequest) (*sandbox.ExecResult, error) {
	sandbox.PrepareEnvVars(&req)

	cfg := &container.Config{
		Image: req.Image,
		Env:   buildEnv(req.EnvVars),
	}

	resources, err := parseResources(req.CPULimit, req.MemLimit)
	if err != nil {
		return nil, err
	}

	promptPath, promptCleanup, err := preparePromptFile(req.Prompt)
	if err != nil {
		return nil, err
	}
	defer promptCleanup()

	binds := make([]string, len(req.Binds), len(req.Binds)+1)
	copy(binds, req.Binds)
	binds = append(binds, promptPath+":/tmp/prompt.txt:ro")
	hostCfg := &container.HostConfig{
		Resources: resources,
		Binds:     binds,
	}

	if req.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(req.Timeout)*time.Second)
		defer cancel()
	}

	if err := r.ensureImage(ctx, req.Image); err != nil {
		return nil, err
	}

	containerID, cleanup, err := r.createContainer(ctx, cfg, hostCfg)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	return r.waitAndCollectLogs(ctx, containerID)
}

func parseResources(cpuLimit, memLimit string) (container.Resources, error) {
	var resources container.Resources
	if cpuLimit != "" {
		cpuFloat, err := strconv.ParseFloat(cpuLimit, 64)
		if err != nil {
			return resources, fmt.Errorf("invalid CPU limit %q: %w", cpuLimit, err)
		}
		resources.NanoCPUs = int64(cpuFloat * 1e9)
	}
	if memLimit != "" {
		mem, err := parseMemLimit(memLimit)
		if err != nil {
			return resources, fmt.Errorf("invalid memory limit %q: %w", memLimit, err)
		}
		resources.Memory = mem
	}
	return resources, nil
}

func preparePromptFile(prompt string) (string, func(), error) {
	f, err := os.CreateTemp("", "prompt-*.txt")
	if err != nil {
		return "", nil, fmt.Errorf("failed to create prompt file: %w", err)
	}
	path := f.Name()
	cleanup := func() { _ = os.Remove(path) }

	if _, err := f.WriteString(prompt); err != nil {
		_ = f.Close()
		cleanup()
		return "", nil, fmt.Errorf("failed to write prompt file: %w", err)
	}
	_ = f.Close()
	if err := os.Chmod(path, 0644); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("failed to chmod prompt file: %w", err)
	}
	return path, cleanup, nil
}

func (r *Runtime) waitAndCollectLogs(ctx context.Context, containerID string) (*sandbox.ExecResult, error) {
	shortID := containerID[:12]

	statusCh, errCh := r.client.ContainerWait(ctx, containerID, container.WaitConditionNotRunning)

	var exitCode int64
	select {
	case err := <-errCh:
		if err != nil {
			return nil, fmt.Errorf("error waiting for container: %w", err)
		}
	case status := <-statusCh:
		exitCode = status.StatusCode
		if status.Error != nil && status.Error.Message != "" {
			return &sandbox.ExecResult{
				ExitCode: int(exitCode),
				Error:    status.Error.Message,
			}, nil
		}
	}

	logReader, err := r.client.ContainerLogs(ctx, containerID, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to read container logs: %w", err)
	}
	defer logReader.Close()

	var stdout, stderr bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdout, &stderr, logReader); err != nil {
		return nil, fmt.Errorf("failed to copy container logs: %w", err)
	}

	if stderr.Len() > 0 {
		slog.Info("sandbox container stderr", "container", shortID, "stderr", stderr.String())
	}

	return &sandbox.ExecResult{
		Output:   stdout.String(),
		ExitCode: int(exitCode),
	}, nil
}

// ensureImage pulls the given image so it is available locally.
func (r *Runtime) ensureImage(ctx context.Context, img string) error {
	pullReader, err := r.client.ImagePull(ctx, img, image.PullOptions{})
	if err != nil {
		return fmt.Errorf("failed to pull image %s: %w", img, err)
	}
	_, _ = io.Copy(io.Discard, pullReader)
	_ = pullReader.Close()
	return nil
}

// createContainer creates and starts a container, returning its ID and a
// cleanup function that removes it.
func (r *Runtime) createContainer(ctx context.Context, cfg *container.Config, hostCfg *container.HostConfig) (string, func(), error) {
	resp, err := r.client.ContainerCreate(ctx, cfg, hostCfg, nil, nil, "")
	if err != nil {
		return "", nil, fmt.Errorf("failed to create container: %w", err)
	}

	shortID := resp.ID[:12]
	slog.Info("sandbox container created", "container", shortID)

	cleanup := func() {
		_ = r.client.ContainerRemove(context.Background(), resp.ID, container.RemoveOptions{Force: true})
		slog.Info("sandbox container removed", "container", shortID)
	}

	if err := r.client.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("failed to start container: %w", err)
	}
	slog.Info("sandbox container started, follow logs with: docker logs -f "+shortID, "container", shortID)

	return resp.ID, cleanup, nil
}

func buildEnv(envVars map[string]string) []string {
	env := make([]string, 0, len(envVars))
	for k, v := range envVars {
		env = append(env, k+"="+v)
	}
	return env
}

func parseMemLimit(s string) (int64, error) {
	if strings.HasSuffix(s, "Gi") {
		val, err := strconv.ParseFloat(strings.TrimSuffix(s, "Gi"), 64)
		if err != nil {
			return 0, err
		}
		return int64(val * 1024 * 1024 * 1024), nil
	}
	if strings.HasSuffix(s, "Mi") {
		val, err := strconv.ParseFloat(strings.TrimSuffix(s, "Mi"), 64)
		if err != nil {
			return 0, err
		}
		return int64(val * 1024 * 1024), nil
	}
	return 0, fmt.Errorf("unsupported memory unit in %q (use Mi or Gi)", s)
}
