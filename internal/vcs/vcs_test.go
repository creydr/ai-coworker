package vcs

import (
	"context"
	"testing"
)

type mockProvider struct {
	name      string
	host      string
	repoMap   map[string]string
	tokenFunc func(repo string) (string, error)
}

func (m *mockProvider) Name() string { return m.name }
func (m *mockProvider) CreateTokenForRepo(_ context.Context, repo string) (string, error) {
	if m.tokenFunc != nil {
		return m.tokenFunc(repo)
	}
	return "token-" + repo, nil
}
func (m *mockProvider) CloneURL(repo string) string {
	return "https://" + m.host + "/" + repo + ".git"
}
func (m *mockProvider) TokenEnvVar() string {
	return m.name + "_TOKEN"
}
func (m *mockProvider) CredentialURL(token string) string {
	return "https://x-access-token:" + token + "@" + m.host
}
func (m *mockProvider) ParseRepoFromURL(rawURL string) (string, bool) {
	for url, repo := range m.repoMap {
		if rawURL == url || rawURL == "https://"+url {
			return repo, true
		}
	}
	return "", false
}

func newGitHubMock() *mockProvider {
	return &mockProvider{
		name: "github",
		host: "github.com",
		repoMap: map[string]string{
			"https://github.com/org/repo1":         "org/repo1",
			"https://github.com/org/repo1/pull/42": "org/repo1",
			"https://github.com/org/repo2":         "org/repo2",
		},
	}
}

func newGitLabMock() *mockProvider {
	return &mockProvider{
		name: "gitlab",
		host: "gitlab.com",
		repoMap: map[string]string{
			"https://gitlab.com/group/project":            "group/project",
			"https://gitlab.com/group/project/-/issues/1": "group/project",
		},
	}
}

func TestResolveURL(t *testing.T) {
	r := NewRegistry()
	r.Register(newGitHubMock())
	r.Register(newGitLabMock())

	tests := []struct {
		url      string
		wantRepo string
		wantName string
		wantOK   bool
	}{
		{"https://github.com/org/repo1", "org/repo1", "github", true},
		{"https://github.com/org/repo1/pull/42", "org/repo1", "github", true},
		{"https://gitlab.com/group/project", "group/project", "gitlab", true},
		{"https://example.com/foo/bar", "", "", false},
	}

	for _, tt := range tests {
		repo, provider, ok := r.ResolveURL(tt.url)
		if ok != tt.wantOK {
			t.Errorf("ResolveURL(%q): ok = %v, want %v", tt.url, ok, tt.wantOK)
			continue
		}
		if !ok {
			continue
		}
		if repo != tt.wantRepo {
			t.Errorf("ResolveURL(%q): repo = %q, want %q", tt.url, repo, tt.wantRepo)
		}
		if provider.Name() != tt.wantName {
			t.Errorf("ResolveURL(%q): provider = %q, want %q", tt.url, provider.Name(), tt.wantName)
		}
	}
}

func TestByName(t *testing.T) {
	r := NewRegistry()
	gh := newGitHubMock()
	gl := newGitLabMock()
	r.Register(gh)
	r.Register(gl)

	p, ok := r.ByName("github")
	if !ok || p.Name() != "github" {
		t.Errorf("ByName(github): got ok=%v, name=%v", ok, p)
	}

	p, ok = r.ByName("gitlab")
	if !ok || p.Name() != "gitlab" {
		t.Errorf("ByName(gitlab): got ok=%v, name=%v", ok, p)
	}

	_, ok = r.ByName("bitbucket")
	if ok {
		t.Error("ByName(bitbucket): expected not found")
	}
}

func TestExtractReposFromText(t *testing.T) {
	r := NewRegistry()
	r.Register(newGitHubMock())
	r.Register(newGitLabMock())

	tests := []struct {
		name string
		text string
		want []struct {
			repo     string
			provider string
		}
	}{
		{
			name: "single github url",
			text: "Please check https://github.com/org/repo1",
			want: []struct {
				repo     string
				provider string
			}{
				{"org/repo1", "github"},
			},
		},
		{
			name: "mixed providers",
			text: "Migrate https://github.com/org/repo1 and https://gitlab.com/group/project to Go 1.26",
			want: []struct {
				repo     string
				provider string
			}{
				{"org/repo1", "github"},
				{"group/project", "gitlab"},
			},
		},
		{
			name: "duplicate urls deduplicated",
			text: "See https://github.com/org/repo1 and also https://github.com/org/repo1/pull/42",
			want: []struct {
				repo     string
				provider string
			}{
				{"org/repo1", "github"},
			},
		},
		{
			name: "no urls",
			text: "Just fix the tests please",
			want: nil,
		},
		{
			name: "unrecognized url ignored",
			text: "Check https://example.com/some/path",
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matches := r.ExtractReposFromText(tt.text)
			if len(matches) != len(tt.want) {
				t.Fatalf("got %d matches, want %d", len(matches), len(tt.want))
			}
			for i, m := range matches {
				if m.Repo != tt.want[i].repo {
					t.Errorf("match[%d].Repo = %q, want %q", i, m.Repo, tt.want[i].repo)
				}
				if m.Provider.Name() != tt.want[i].provider {
					t.Errorf("match[%d].Provider = %q, want %q", i, m.Provider.Name(), tt.want[i].provider)
				}
			}
		})
	}
}

func TestAll(t *testing.T) {
	r := NewRegistry()
	if len(r.All()) != 0 {
		t.Error("empty registry should return no providers")
	}
	r.Register(newGitHubMock())
	r.Register(newGitLabMock())
	if len(r.All()) != 2 {
		t.Errorf("expected 2 providers, got %d", len(r.All()))
	}
}
