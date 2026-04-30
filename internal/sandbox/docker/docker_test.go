package docker

import (
	"sort"
	"strings"
	"testing"

	"github.com/creydr/ai-coworker/internal/sandbox"
)

func TestBuildCmd_NoCloneURL(t *testing.T) {
	req := sandbox.ExecRequest{
		Prompt: "say hello",
	}

	cmd := buildCmd(req)

	if len(cmd) != 1 {
		t.Fatalf("expected 1 element, got %d: %v", len(cmd), cmd)
	}
	if cmd[0] != "say hello" {
		t.Errorf("expected %q, got %q", "say hello", cmd[0])
	}
}

func TestBuildCmd_WithCloneURL(t *testing.T) {
	req := sandbox.ExecRequest{
		CloneURL: "https://github.com/example/repo.git",
		Prompt:   "fix the bug",
	}

	cmd := buildCmd(req)

	if len(cmd) != 3 {
		t.Fatalf("expected 3 elements, got %d: %v", len(cmd), cmd)
	}
	if cmd[0] != "/bin/sh" {
		t.Errorf("expected cmd[0] = /bin/sh, got %q", cmd[0])
	}
	if cmd[1] != "-c" {
		t.Errorf("expected cmd[1] = -c, got %q", cmd[1])
	}

	script := cmd[2]

	// The CloneURL should be shell-escaped (wrapped in single quotes).
	escapedURL := "'https://github.com/example/repo.git'"
	if !strings.Contains(script, escapedURL) {
		t.Errorf("expected script to contain shell-escaped URL %q, got %q", escapedURL, script)
	}

	// Should not contain -b since no branch is set.
	if strings.Contains(script, " -b ") {
		t.Errorf("expected no -b flag when branch is empty, got %q", script)
	}

	// Should contain the clone command and the claude command.
	if !strings.Contains(script, "git clone") {
		t.Errorf("expected script to contain 'git clone', got %q", script)
	}
	if !strings.Contains(script, "claude --dangerously-skip-permissions") {
		t.Errorf("expected script to contain claude command, got %q", script)
	}
}

func TestBuildCmd_WithCloneURLAndBranch(t *testing.T) {
	req := sandbox.ExecRequest{
		CloneURL: "https://github.com/example/repo.git",
		Branch:   "feature/my-branch",
		Prompt:   "refactor code",
	}

	cmd := buildCmd(req)

	if len(cmd) != 3 {
		t.Fatalf("expected 3 elements, got %d: %v", len(cmd), cmd)
	}

	script := cmd[2]

	// Should contain -b <branch>.
	if !strings.Contains(script, " -b feature/my-branch") {
		t.Errorf("expected script to contain '-b feature/my-branch', got %q", script)
	}

	// The CloneURL should still be shell-escaped.
	if !strings.Contains(script, "'https://github.com/example/repo.git'") {
		t.Errorf("expected script to contain shell-escaped URL, got %q", script)
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

func TestShellescape(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "normal string",
			input:    "hello",
			expected: "'hello'",
		},
		{
			name:     "string with single quotes",
			input:    "it's a test",
			expected: "'it'\\''s a test'",
		},
		{
			name:     "string with spaces",
			input:    "hello world",
			expected: "'hello world'",
		},
		{
			name:     "empty string",
			input:    "",
			expected: "''",
		},
		{
			name:     "url",
			input:    "https://github.com/example/repo.git",
			expected: "'https://github.com/example/repo.git'",
		},
		{
			name:     "multiple single quotes",
			input:    "a'b'c",
			expected: "'a'\\''b'\\''c'",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := shellescape(tt.input)
			if result != tt.expected {
				t.Errorf("shellescape(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
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
