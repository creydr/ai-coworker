package docker

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/docker/docker/api/types/container"
)

func TestShortID(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"abcdef123456789", "abcdef123456"},
		{"abcdef123456", "abcdef123456"},
		{"short", "short"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := shortID(tt.input); got != tt.want {
			t.Errorf("shortID(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestBuildEnv(t *testing.T) {
	tests := []struct {
		name     string
		envVars  map[string]string
		expected []string
	}{
		{
			name:     "nil map",
			envVars:  nil,
			expected: []string{},
		},
		{
			name:     "empty map",
			envVars:  map[string]string{},
			expected: []string{},
		},
		{
			name: "single entry",
			envVars: map[string]string{
				"FOO": "bar",
			},
			expected: []string{"FOO=bar"},
		},
		{
			name: "multiple entries",
			envVars: map[string]string{
				"FOO":   "bar",
				"TOKEN": "secret123",
			},
			expected: []string{"FOO=bar", "TOKEN=secret123"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := buildEnv(tt.envVars)

			if len(result) != len(tt.expected) {
				t.Fatalf("expected %d entries, got %d: %v", len(tt.expected), len(result), result)
			}

			// Sort both slices for deterministic comparison since map iteration order is random.
			sort.Strings(result)
			sort.Strings(tt.expected)

			for i := range tt.expected {
				if result[i] != tt.expected[i] {
					t.Errorf("entry %d: expected %q, got %q", i, tt.expected[i], result[i])
				}
			}
		})
	}
}

func TestParseResources(t *testing.T) {
	t.Run("valid CPU and memory", func(t *testing.T) {
		res, err := parseResources("2.0", "4Gi")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.NanoCPUs != 2e9 {
			t.Errorf("NanoCPUs = %d, want %d", res.NanoCPUs, int64(2e9))
		}
		if res.Memory != 4*1024*1024*1024 {
			t.Errorf("Memory = %d, want %d", res.Memory, int64(4*1024*1024*1024))
		}
	})

	t.Run("empty limits", func(t *testing.T) {
		res, err := parseResources("", "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.NanoCPUs != 0 || res.Memory != 0 {
			t.Errorf("expected zero resources, got CPU=%d, Mem=%d", res.NanoCPUs, res.Memory)
		}
	})

	t.Run("invalid CPU", func(t *testing.T) {
		_, err := parseResources("abc", "")
		if err == nil {
			t.Fatal("expected error for invalid CPU limit")
		}
	})

	t.Run("invalid memory", func(t *testing.T) {
		_, err := parseResources("", "invalid")
		if err == nil {
			t.Fatal("expected error for invalid memory limit")
		}
	})
}

func TestPreparePromptFile(t *testing.T) {
	path, cleanup, err := preparePromptFile("test prompt content")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer cleanup()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read prompt file: %v", err)
	}
	if string(data) != "test prompt content" {
		t.Errorf("prompt file content = %q, want %q", string(data), "test prompt content")
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("failed to stat prompt file: %v", err)
	}
	if info.Mode().Perm() != 0644 {
		t.Errorf("prompt file permissions = %o, want 0644", info.Mode().Perm())
	}
}

func TestHostConfig_SecurityHardening(t *testing.T) {
	resources, err := parseResources("2.0", "4Gi")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	binds := []string{"/tmp/prompt.txt:/tmp/prompt.txt:ro"}
	hc := &container.HostConfig{
		Resources:   resources,
		Binds:       binds,
		CapDrop:     []string{"ALL"},
		SecurityOpt: []string{"no-new-privileges"},
	}

	if len(hc.CapDrop) != 1 || hc.CapDrop[0] != "ALL" {
		t.Errorf("CapDrop = %v, want [ALL]", hc.CapDrop)
	}
	if len(hc.SecurityOpt) != 1 || hc.SecurityOpt[0] != "no-new-privileges" {
		t.Errorf("SecurityOpt = %v, want [no-new-privileges]", hc.SecurityOpt)
	}
	if hc.NanoCPUs != 2e9 {
		t.Errorf("NanoCPUs = %d, want %d", hc.NanoCPUs, int64(2e9))
	}
	if len(hc.Binds) != 1 {
		t.Errorf("Binds = %v, want 1 entry", hc.Binds)
	}
}

func TestParseMemLimit(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		expected  int64
		expectErr bool
	}{
		{
			name:     "2Gi",
			input:    "2Gi",
			expected: 2 * 1024 * 1024 * 1024,
		},
		{
			name:     "1Gi",
			input:    "1Gi",
			expected: 1024 * 1024 * 1024,
		},
		{
			name:     "512Mi",
			input:    "512Mi",
			expected: 512 * 1024 * 1024,
		},
		{
			name:     "256Mi",
			input:    "256Mi",
			expected: 256 * 1024 * 1024,
		},
		{
			name:      "invalid suffix",
			input:     "100Ki",
			expectErr: true,
		},
		{
			name:      "no unit",
			input:     "1024",
			expectErr: true,
		},
		{
			name:      "invalid number with Gi",
			input:     "abcGi",
			expectErr: true,
		},
		{
			name:      "invalid number with Mi",
			input:     "xyzMi",
			expectErr: true,
		},
		{
			name:      "empty string",
			input:     "",
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := parseMemLimit(tt.input)

			if tt.expectErr {
				if err == nil {
					t.Fatalf("expected error for input %q, got result %d", tt.input, result)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error for input %q: %v", tt.input, err)
			}

			if result != tt.expected {
				t.Errorf("parseMemLimit(%q) = %d, want %d", tt.input, result, tt.expected)
			}
		})
	}
}

func TestExtractTar(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	files := []struct {
		name    string
		content string
	}{
		{"skills/my-skill/skill.md", "# My Skill\n"},
		{"skills/my-skill/helper.sh", "#!/bin/sh\necho hello\n"},
	}

	for _, f := range files {
		hdr := &tar.Header{
			Name: f.name,
			Mode: 0644,
			Size: int64(len(f.content)),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(f.content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	if err := extractTar(&buf, dir); err != nil {
		t.Fatalf("extractTar failed: %v", err)
	}

	for _, f := range files {
		path := filepath.Join(dir, f.name)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("expected file %s to exist: %v", f.name, err)
			continue
		}
		if string(data) != f.content {
			t.Errorf("file %s content = %q, want %q", f.name, string(data), f.content)
		}
	}
}

func TestCleanupSkillDirs(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()

	if _, err := os.Stat(dir1); err != nil {
		t.Fatalf("dir1 should exist before cleanup: %v", err)
	}

	cleanupSkillDirs([]string{dir1, dir2})

	if _, err := os.Stat(dir1); !os.IsNotExist(err) {
		t.Errorf("dir1 should be removed after cleanup")
	}
	if _, err := os.Stat(dir2); !os.IsNotExist(err) {
		t.Errorf("dir2 should be removed after cleanup")
	}
}

func TestCleanupSkillDirsEmpty(t *testing.T) {
	cleanupSkillDirs(nil)
	cleanupSkillDirs([]string{})
}

func TestExtractTarPathTraversal(t *testing.T) {
	tests := []struct {
		name string
		path string
	}{
		{"dotdot prefix", "../../etc/passwd"},
		{"dotdot nested", "skills/../../../etc/shadow"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			tw := tar.NewWriter(&buf)
			hdr := &tar.Header{
				Name: tt.path,
				Mode: 0644,
				Size: 5,
			}
			if err := tw.WriteHeader(hdr); err != nil {
				t.Fatal(err)
			}
			if _, err := tw.Write([]byte("pwned")); err != nil {
				t.Fatal(err)
			}
			if err := tw.Close(); err != nil {
				t.Fatal(err)
			}

			dir := t.TempDir()
			err := extractTar(&buf, dir)
			if err == nil {
				t.Fatal("expected error for path traversal, got nil")
			}
			if !bytes.Contains([]byte(err.Error()), []byte("escapes destination")) {
				t.Errorf("unexpected error message: %v", err)
			}
		})
	}
}

func TestExtractTarSymlink(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	hdr := &tar.Header{
		Name: "skills/shared.md",
		Mode: 0644,
		Size: 7,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("shared\n")); err != nil {
		t.Fatal(err)
	}

	hdr = &tar.Header{
		Typeflag: tar.TypeSymlink,
		Name:     "skills/link.md",
		Linkname: "shared.md",
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	if err := extractTar(&buf, dir); err != nil {
		t.Fatalf("extractTar failed: %v", err)
	}

	target, err := os.Readlink(filepath.Join(dir, "skills/link.md"))
	if err != nil {
		t.Fatalf("expected symlink to exist: %v", err)
	}
	if target != "shared.md" {
		t.Errorf("symlink target = %q, want %q", target, "shared.md")
	}
}

func TestExtractTarSymlinkTraversal(t *testing.T) {
	tests := []struct {
		name     string
		linkname string
	}{
		{"absolute target", "/etc/passwd"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			tw := tar.NewWriter(&buf)
			hdr := &tar.Header{
				Typeflag: tar.TypeSymlink,
				Name:     "skills/evil-link",
				Linkname: tt.linkname,
			}
			if err := tw.WriteHeader(hdr); err != nil {
				t.Fatal(err)
			}
			if err := tw.Close(); err != nil {
				t.Fatal(err)
			}

			dir := t.TempDir()
			err := extractTar(&buf, dir)
			if err == nil {
				t.Fatal("expected error for symlink traversal, got nil")
			}
		})
	}
}

func TestExtractTarSymlinkRelativeTraversal(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	hdr := &tar.Header{
		Typeflag: tar.TypeSymlink,
		Name:     "skills/evil-link",
		Linkname: "../../etc/passwd",
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	err := extractTar(&buf, dir)
	if err == nil {
		t.Fatal("expected error for relative symlink traversal, got nil")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("escapes destination")) {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestExtractTarOversizedFile(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	hdr := &tar.Header{
		Name: "skills/huge.bin",
		Mode: 0644,
		Size: maxSkillFileSize + 1,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	_ = tw.Close() // intentionally incomplete tar entry

	dir := t.TempDir()
	err := extractTar(&buf, dir)
	if err == nil {
		t.Fatal("expected error for oversized file, got nil")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("exceeds")) {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestExtractTarOversizedActualData(t *testing.T) {
	oversized := make([]byte, maxSkillFileSize+1)
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	hdr := &tar.Header{
		Name: "skills/sneaky.bin",
		Mode: 0644,
		Size: int64(len(oversized)),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(oversized); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	err := extractTar(&buf, dir)
	if err == nil {
		t.Fatal("expected error for oversized actual data, got nil")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("exceeds")) {
		t.Errorf("unexpected error message: %v", err)
	}
}
