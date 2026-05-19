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
	"golang.org/x/sync/singleflight"

	"github.com/creydr/ai-coworker/internal/vcs"
)

var _ vcs.Provider = (*Provider)(nil)

// Provider implements vcs.Provider for GitHub repositories.
type Provider struct {
	appsTransport     *ghinstallation.AppsTransport
	apiBaseURL        string
	webHost           string
	repoInstallations sync.Map
	discoverGroup     singleflight.Group
}

// New creates a new GitHub VCS provider.
func New(appsTransport *ghinstallation.AppsTransport, apiBaseURL string) *Provider {
	webHost := "github.com"
	if apiBaseURL != "" {
		if u, err := url.Parse(apiBaseURL); err == nil && u.Host != "" {
			webHost = u.Host
		}
	}
	return &Provider{appsTransport: appsTransport, apiBaseURL: apiBaseURL, webHost: webHost}
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
	if u.Host != p.webHost {
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

	_, repoName, err := SplitRepo(fullRepo)
	if err != nil {
		return "", err
	}

	appClient := p.newAppClient()
	token, _, err := appClient.Apps.CreateInstallationToken(ctx, installationID, &gh.InstallationTokenOptions{
		Repositories: []string{repoName},
	})
	if err != nil {
		return "", fmt.Errorf("failed to create installation token: %w", err)
	}
	return token.GetToken(), nil
}

func (p *Provider) CloneURL(repo string) string {
	return fmt.Sprintf("https://%s/%s.git", p.webHost, repo)
}

func (p *Provider) TokenEnvVar() string { return "GITHUB_TOKEN" }

func (p *Provider) CredentialURL(token string) string {
	return fmt.Sprintf("https://x-access-token:%s@%s", token, p.webHost)
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
	v, err, _ := p.discoverGroup.Do(repo, func() (interface{}, error) {
		return p.discoverInstallation(ctx, repo)
	})
	if err != nil {
		return 0, err
	}
	return v.(int64), nil
}

func (p *Provider) discoverInstallation(ctx context.Context, fullRepo string) (int64, error) {
	owner, repo, err := SplitRepo(fullRepo)
	if err != nil {
		return 0, err
	}
	appClient := p.newAppClient()
	inst, _, err := appClient.Apps.FindRepositoryInstallation(ctx, owner, repo)
	if err != nil {
		return 0, fmt.Errorf("no GitHub App installation for %s: %w", fullRepo, err)
	}
	id := inst.GetID()
	p.repoInstallations.Store(fullRepo, id)
	return id, nil
}

func (p *Provider) newAppClient() *gh.Client {
	client := gh.NewClient(&http.Client{Transport: p.appsTransport})
	if p.apiBaseURL != "" {
		if baseURL, err := url.Parse(strings.TrimRight(p.apiBaseURL, "/") + "/"); err == nil {
			client.BaseURL = baseURL
		}
	}
	return client
}

func SplitRepo(fullName string) (owner, repo string, err error) {
	parts := strings.SplitN(fullName, "/", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("invalid repo format %q: expected owner/repo", fullName)
	}
	return parts[0], parts[1], nil
}
