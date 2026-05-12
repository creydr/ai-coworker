package docker

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
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

	binds := make([]string, len(req.Binds), len(req.Binds)+1+len(req.SkillImages))
	copy(binds, req.Binds)
	binds = append(binds, promptPath+":/tmp/prompt.txt:ro")

	var skillDirs []string
	for i, img := range req.SkillImages {
		if err := r.ensureImage(ctx, img); err != nil {
			cleanupSkillDirs(skillDirs)
			return nil, fmt.Errorf("failed to pull skill image %s: %w", img, err)
		}
		dir, err := r.extractSkillImage(ctx, img, i)
		if err != nil {
			cleanupSkillDirs(skillDirs)
			return nil, fmt.Errorf("failed to extract skill image %s: %w", img, err)
		}
		skillDirs = append(skillDirs, dir)
		binds = append(binds, fmt.Sprintf("%s:/opt/skills-%d:ro", dir, i))
	}
	defer cleanupSkillDirs(skillDirs)

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

// ensureImage pulls the given image so it is available locally, retrying on
// transient errors up to 3 times with exponential backoff.
func (r *Runtime) ensureImage(ctx context.Context, img string) error {
	var lastErr error
	for attempt := range 3 {
		if attempt > 0 {
			backoff := time.Duration(1<<(attempt-1)) * time.Second
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
		}
		pullReader, err := r.client.ImagePull(ctx, img, image.PullOptions{})
		if err != nil {
			lastErr = err
			slog.Warn("image pull failed, retrying", "image", img, "attempt", attempt+1, "error", err)
			continue
		}
		_, _ = io.Copy(io.Discard, pullReader)
		_ = pullReader.Close()
		return nil
	}
	return fmt.Errorf("failed to pull image %s: %w", img, lastErr)
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

// extractSkillImage creates a throwaway container from the skill image and
// copies its /skills directory to a temporary host directory.
func (r *Runtime) extractSkillImage(ctx context.Context, img string, index int) (string, error) {
	resp, err := r.client.ContainerCreate(ctx, &container.Config{Image: img}, nil, nil, nil, "")
	if err != nil {
		return "", fmt.Errorf("failed to create container from skill image: %w", err)
	}
	defer func() {
		if err := r.client.ContainerRemove(context.Background(), resp.ID, container.RemoveOptions{}); err != nil {
			slog.Warn("failed to remove skill extraction container", "id", resp.ID, "error", err)
		}
	}()

	dir, err := os.MkdirTemp("", fmt.Sprintf("ai-coworker-skills-%d-*", index))
	if err != nil {
		return "", fmt.Errorf("failed to create temp dir for skill image: %w", err)
	}

	reader, _, err := r.client.CopyFromContainer(ctx, resp.ID, "/skills")
	if err != nil {
		_ = os.RemoveAll(dir)
		return "", fmt.Errorf("failed to copy /skills from image: %w", err)
	}
	defer reader.Close()

	if err := extractTar(reader, dir); err != nil {
		_ = os.RemoveAll(dir)
		return "", fmt.Errorf("failed to extract skill files: %w", err)
	}

	slog.Info("extracted skill image", "image", img, "dir", dir)
	return dir, nil
}

func cleanupSkillDirs(dirs []string) {
	for _, d := range dirs {
		_ = os.RemoveAll(d)
	}
}

const maxSkillFileSize = 10 << 20 // 10 MiB per file

func extractTar(r io.Reader, dst string) error {
	cleanDst := filepath.Clean(dst) + string(os.PathSeparator)
	tr := tar.NewReader(r)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}

		target := filepath.Join(dst, header.Name)
		if !strings.HasPrefix(filepath.Clean(target)+string(os.PathSeparator), cleanDst) &&
			filepath.Clean(target) != filepath.Clean(dst) {
			return fmt.Errorf("tar entry %q escapes destination directory", header.Name)
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
		case tar.TypeReg:
			if header.Size > maxSkillFileSize {
				return fmt.Errorf("tar entry %q is %d bytes, exceeds %d byte limit", header.Name, header.Size, maxSkillFileSize)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			f, err := os.Create(target)
			if err != nil {
				return err
			}
			n, err := io.Copy(f, io.LimitReader(tr, maxSkillFileSize+1))
			if err != nil {
				_ = f.Close()
				return err
			}
			_ = f.Close()
			if n > maxSkillFileSize {
				return fmt.Errorf("tar entry %q exceeds %d byte limit", header.Name, maxSkillFileSize)
			}
		case tar.TypeSymlink:
			linkTarget := header.Linkname
			if filepath.IsAbs(linkTarget) {
				return fmt.Errorf("tar symlink %q has absolute target %q", header.Name, linkTarget)
			}
			resolved := filepath.Join(filepath.Dir(target), linkTarget)
			if !strings.HasPrefix(filepath.Clean(resolved)+string(os.PathSeparator), cleanDst) &&
				filepath.Clean(resolved) != filepath.Clean(dst) {
				return fmt.Errorf("tar symlink %q target %q escapes destination directory", header.Name, linkTarget)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			if err := os.Symlink(linkTarget, target); err != nil {
				return err
			}
		}
	}
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
