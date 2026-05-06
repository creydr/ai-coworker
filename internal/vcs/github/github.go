package github

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"

	ghinstallation "github.com/bradleyfalzon/ghinstallation/v2"
	gh "github.com/google/go-github/v68/github"

	"github.com/creydr/ai-coworker/internal/vcs"
)

var _ vcs.Provider = (*Provider)(nil)

// Provider implements vcs.Provider for GitHub repositories.
type Provider struct {
	appsTransport     *ghinstallation.AppsTransport
	repoInstallations sync.Map
}

// New creates a new GitHub VCS provider.
func New(appsTransport *ghinstallation.AppsTransport) *Provider {
	return &Provider{appsTransport: appsTransport}
}

func (p *Provider) Name() string { return "github" }

func (p *Provider) ParseRepoFromURL(rawURL string) (string, bool) {
	if !strings.Contains(rawURL, "://") {
		rawURL = "https://" + rawURL
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", false
	}
	if u.Host != "github.com" {
		return "", false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 {
		return "", false
	}
	return parts[0] + "/" + parts[1], true
}

func (p *Provider) CreateTokenForRepo(ctx context.Context, fullRepo string) (string, error) {
	installationID, err := p.GetInstallationID(ctx, fullRepo)
	if err != nil {
		return "", err
	}

	_, repoName, err := splitRepo(fullRepo)
	if err != nil {
		return "", err
	}

	appClient := gh.NewClient(&http.Client{Transport: p.appsTransport})
	token, _, err := appClient.Apps.CreateInstallationToken(ctx, installationID, &gh.InstallationTokenOptions{
		Repositories: []string{repoName},
	})
	if err != nil {
		return "", fmt.Errorf("failed to create installation token: %w", err)
	}
	return token.GetToken(), nil
}

func (p *Provider) CloneURL(repo string) string {
	return fmt.Sprintf("https://github.com/%s.git", repo)
}

func (p *Provider) TokenEnvVar() string { return "GITHUB_TOKEN" }

func (p *Provider) CredentialURL(token string) string {
	return fmt.Sprintf("https://x-access-token:%s@github.com", token)
}

// TrackInstallation caches the installation ID for a repo. Called by the
// GitHub adapter when it receives webhook events.
func (p *Provider) TrackInstallation(repo string, installationID int64) {
	p.repoInstallations.Store(repo, installationID)
}

// GetInstallationID returns the installation ID for a repo, discovering it
// via the GitHub API if not cached.
func (p *Provider) GetInstallationID(ctx context.Context, repo string) (int64, error) {
	if v, ok := p.repoInstallations.Load(repo); ok {
		return v.(int64), nil
	}
	return p.discoverInstallation(ctx, repo)
}

func (p *Provider) discoverInstallation(ctx context.Context, fullRepo string) (int64, error) {
	owner, repo, err := splitRepo(fullRepo)
	if err != nil {
		return 0, err
	}
	appClient := gh.NewClient(&http.Client{Transport: p.appsTransport})
	inst, _, err := appClient.Apps.FindRepositoryInstallation(ctx, owner, repo)
	if err != nil {
		return 0, fmt.Errorf("no GitHub App installation for %s: %w", fullRepo, err)
	}
	id := inst.GetID()
	p.repoInstallations.Store(fullRepo, id)
	return id, nil
}

func splitRepo(fullName string) (owner, repo string, err error) {
	parts := strings.SplitN(fullName, "/", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("invalid repo format %q: expected owner/repo", fullName)
	}
	return parts[0], parts[1], nil
}
