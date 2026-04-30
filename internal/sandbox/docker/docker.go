package docker

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/creydr/ai-coworker/internal/sandbox"
	"github.com/docker/docker/api/types/container"
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
	env := buildEnv(req.EnvVars)

	cmd := buildCmd(req)

	cfg := &container.Config{
		Image: req.Image,
		Cmd:   cmd,
		Env:   env,
	}

	hostCfg := &container.HostConfig{}

	resp, err := r.client.ContainerCreate(ctx, cfg, hostCfg, nil, nil, "")
	if err != nil {
		return nil, fmt.Errorf("failed to create container: %w", err)
	}

	defer func() {
		_ = r.client.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
	}()

	if err := r.client.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return nil, fmt.Errorf("failed to start container: %w", err)
	}

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

	output := stdout.String()
	if stderr.Len() > 0 {
		output += "\n" + stderr.String()
	}

	return &sandbox.ExecResult{
		Output:   output,
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

func buildCmd(req sandbox.ExecRequest) []string {
	if req.CloneURL == "" {
		return []string{req.Prompt}
	}

	cloneCmd := "git clone"
	if req.Branch != "" {
		cloneCmd += " -b " + req.Branch
	}
	cloneCmd += " " + req.CloneURL + " /workspace/repo && cd /workspace/repo"

	shellScript := strings.Join([]string{
		cloneCmd,
		fmt.Sprintf("claude --dangerously-skip-permissions -p %q", req.Prompt),
	}, " && ")

	return []string{"/bin/sh", "-c", shellScript}
}
