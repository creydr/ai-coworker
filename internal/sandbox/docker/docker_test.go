package docker

import (
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
