package github

import (
	"testing"
)

func TestParseRepoFromURL(t *testing.T) {
	p := &Provider{}

	tests := []struct {
		url      string
		wantRepo string
		wantOK   bool
	}{
		{"https://github.com/org/repo", "org/repo", true},
		{"https://github.com/org/repo/pull/42", "org/repo", true},
		{"https://github.com/org/repo/issues/7", "org/repo", true},
		{"https://github.com/org/repo/tree/main/src", "org/repo", true},
		{"github.com/org/repo", "org/repo", true},
		{"https://gitlab.com/org/repo", "", false},
		{"https://bitbucket.org/org/repo", "", false},
		{"https://github.com/", "", false},
		{"https://github.com/org", "", false},
		{"not-a-url", "", false},
	}

	for _, tt := range tests {
		repo, ok := p.ParseRepoFromURL(tt.url)
		if ok != tt.wantOK {
			t.Errorf("ParseRepoFromURL(%q): ok = %v, want %v", tt.url, ok, tt.wantOK)
			continue
		}
		if repo != tt.wantRepo {
			t.Errorf("ParseRepoFromURL(%q): repo = %q, want %q", tt.url, repo, tt.wantRepo)
		}
	}
}

func TestCloneURL(t *testing.T) {
	p := &Provider{}
	got := p.CloneURL("org/repo")
	want := "https://github.com/org/repo.git"
	if got != want {
		t.Errorf("CloneURL = %q, want %q", got, want)
	}
}

func TestTokenEnvVar(t *testing.T) {
	p := &Provider{}
	if p.TokenEnvVar() != "GITHUB_TOKEN" {
		t.Errorf("TokenEnvVar = %q, want GITHUB_TOKEN", p.TokenEnvVar())
	}
}

func TestCredentialURL(t *testing.T) {
	p := &Provider{}
	got := p.CredentialURL("test-token")
	want := "https://x-access-token:test-token@github.com"
	if got != want {
		t.Errorf("CredentialURL = %q, want %q", got, want)
	}
}

func TestName(t *testing.T) {
	p := &Provider{}
	if p.Name() != "github" {
		t.Errorf("Name = %q, want github", p.Name())
	}
}

func TestSplitRepo(t *testing.T) {
	tests := []struct {
		input     string
		wantOwner string
		wantRepo  string
		wantErr   bool
	}{
		{"org/repo", "org", "repo", false},
		{"my-org/my-repo", "my-org", "my-repo", false},
		{"invalid", "", "", true},
		{"", "", "", true},
	}

	for _, tt := range tests {
		owner, repo, err := SplitRepo(tt.input)
		if (err != nil) != tt.wantErr {
			t.Errorf("SplitRepo(%q): err = %v, wantErr = %v", tt.input, err, tt.wantErr)
			continue
		}
		if owner != tt.wantOwner || repo != tt.wantRepo {
			t.Errorf("SplitRepo(%q) = (%q, %q), want (%q, %q)", tt.input, owner, repo, tt.wantOwner, tt.wantRepo)
		}
	}
}

func TestTrackAndGetInstallationID_Cached(t *testing.T) {
	p := &Provider{}
	p.TrackInstallation("org/repo", 42)

	id, err := p.GetInstallationID(t.Context(), "org/repo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != 42 {
		t.Errorf("GetInstallationID = %d, want 42", id)
	}
}
