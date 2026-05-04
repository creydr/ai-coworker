package store

import (
	"encoding/json"
	"testing"
)

func TestEncodeMetadata(t *testing.T) {
	tests := []struct {
		name string
		in   map[string]string
		want string
	}{
		{
			name: "nil map",
			in:   nil,
			want: "{}",
		},
		{
			name: "empty map",
			in:   map[string]string{},
			want: "{}",
		},
		{
			name: "single entry",
			in:   map[string]string{"repo": "org/repo"},
		},
		{
			name: "multiple entries",
			in:   map[string]string{"repo": "org/repo", "is_pr": "true", "pr_branch": "feat/x"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := encodeMetadata(tt.in)
			if err != nil {
				t.Fatalf("encodeMetadata: %v", err)
			}
			if tt.want != "" {
				if string(data) != tt.want {
					t.Errorf("got %q, want %q", string(data), tt.want)
				}
				return
			}
			var m map[string]string
			if err := json.Unmarshal(data, &m); err != nil {
				t.Fatalf("result is not valid JSON: %v", err)
			}
			for k, v := range tt.in {
				if m[k] != v {
					t.Errorf("key %q: got %q, want %q", k, m[k], v)
				}
			}
		})
	}
}

func TestDecodeMetadata(t *testing.T) {
	tests := []struct {
		name string
		in   []byte
		want map[string]string
	}{
		{
			name: "empty bytes",
			in:   []byte{},
			want: map[string]string{},
		},
		{
			name: "empty object",
			in:   []byte("{}"),
			want: map[string]string{},
		},
		{
			name: "populated",
			in:   []byte(`{"repo":"org/repo","is_pr":"true"}`),
			want: map[string]string{"repo": "org/repo", "is_pr": "true"},
		},
		{
			name: "null json",
			in:   []byte("null"),
			want: map[string]string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, err := decodeMetadata(tt.in)
			if err != nil {
				t.Fatalf("decodeMetadata: %v", err)
			}
			if m == nil {
				t.Fatal("decodeMetadata returned nil, want non-nil map")
			}
			if len(m) != len(tt.want) {
				t.Errorf("got %d entries, want %d", len(m), len(tt.want))
			}
			for k, v := range tt.want {
				if m[k] != v {
					t.Errorf("key %q: got %q, want %q", k, m[k], v)
				}
			}
		})
	}
}

func TestEncodeDecodeRoundtrip(t *testing.T) {
	original := map[string]string{
		"repo":       "creydr/test",
		"is_pr":      "true",
		"pr_branch":  "feat/cool-thing",
		"comment_id": "12345",
		"type":       "review_comment",
	}

	data, err := encodeMetadata(original)
	if err != nil {
		t.Fatalf("encodeMetadata: %v", err)
	}

	decoded, err := decodeMetadata(data)
	if err != nil {
		t.Fatalf("decodeMetadata: %v", err)
	}

	if len(decoded) != len(original) {
		t.Fatalf("roundtrip produced %d entries, want %d", len(decoded), len(original))
	}
	for k, v := range original {
		if decoded[k] != v {
			t.Errorf("key %q: got %q, want %q", k, decoded[k], v)
		}
	}
}
