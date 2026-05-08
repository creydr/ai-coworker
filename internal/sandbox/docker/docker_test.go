package docker

import (
	"os"
	"sort"
	"testing"
)

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
