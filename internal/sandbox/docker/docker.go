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

	"github.com/creydr/ai-coworker/internal/sandbox"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
)

var _ sandbox.Runtime = (*Runtime)(nil)

type Runtime struct {
	client *client.Client
}

func New() (*Runtime, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("failed to create docker client: %w", err)
	}
	return &Runtime{client: cli}, nil
}

func (r *Runtime) Exec(ctx context.Context, req sandbox.ExecRequest) (*sandbox.ExecResult, error) {
	if req.EnvVars == nil {
		req.EnvVars = make(map[string]string)
	}
	if req.CloneURL != "" {
		req.EnvVars["CLONE_URL"] = req.CloneURL
		if req.Branch != "" {
			req.EnvVars["CLONE_BRANCH"] = req.Branch
		}
	}

	env := buildEnv(req.EnvVars)

	cfg := &container.Config{
		Image: req.Image,
		Env:   env,
	}

	var resources container.Resources

	if req.CPULimit != "" {
		cpuFloat, err := strconv.ParseFloat(req.CPULimit, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid CPU limit %q: %w", req.CPULimit, err)
		}
		resources.NanoCPUs = int64(cpuFloat * 1e9)
	}

	if req.MemLimit != "" {
		mem, err := parseMemLimit(req.MemLimit)
		if err != nil {
			return nil, fmt.Errorf("invalid memory limit %q: %w", req.MemLimit, err)
		}
		resources.Memory = mem
	}

	promptFile, err := os.CreateTemp("", "prompt-*.txt")
	if err != nil {
		return nil, fmt.Errorf("failed to create prompt file: %w", err)
	}
	defer os.Remove(promptFile.Name())
	if _, err := promptFile.WriteString(req.Prompt); err != nil {
		promptFile.Close()
		return nil, fmt.Errorf("failed to write prompt file: %w", err)
	}
	promptFile.Close()
	if err := os.Chmod(promptFile.Name(), 0644); err != nil {
		return nil, fmt.Errorf("failed to chmod prompt file: %w", err)
	}

	binds := make([]string, len(req.Binds), len(req.Binds)+1)
	copy(binds, req.Binds)
	binds = append(binds, promptFile.Name()+":/tmp/prompt.txt:ro")
	hostCfg := &container.HostConfig{
		Resources: resources,
		Binds:     binds,
	}

	if req.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(req.Timeout)*time.Second)
		defer cancel()
	}

	pullReader, err := r.client.ImagePull(ctx, req.Image, image.PullOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to pull image %s: %w", req.Image, err)
	}
	_, _ = io.Copy(io.Discard, pullReader)
	pullReader.Close()

	resp, err := r.client.ContainerCreate(ctx, cfg, hostCfg, nil, nil, "")
	if err != nil {
		return nil, fmt.Errorf("failed to create container: %w", err)
	}

	shortID := resp.ID[:12]
	slog.Info("sandbox container created", "container", shortID)

	defer func() {
		_ = r.client.ContainerRemove(context.Background(), resp.ID, container.RemoveOptions{Force: true})
		slog.Info("sandbox container removed", "container", shortID)
	}()

	if err := r.client.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return nil, fmt.Errorf("failed to start container: %w", err)
	}
	slog.Info("sandbox container started, follow logs with: docker logs -f "+shortID, "container", shortID)

	statusCh, errCh := r.client.ContainerWait(ctx, resp.ID, container.WaitConditionNotRunning)

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

	logReader, err := r.client.ContainerLogs(ctx, resp.ID, container.LogsOptions{
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
