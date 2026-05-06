package vcs

import (
	"context"
	"fmt"
	"regexp"
)

// Provider abstracts VCS platform operations (token creation, clone URLs, credential setup).
type Provider interface {
	Name() string
	CreateTokenForRepo(ctx context.Context, repo string) (string, error)
	CloneURL(repo string) string
	TokenEnvVar() string
	CredentialURL(token string) string
	ParseRepoFromURL(rawURL string) (repo string, ok bool)
}

// RepoMatch pairs a parsed repository identifier with the provider that owns it.
type RepoMatch struct {
	Repo     string
	Provider Provider
}

// Registry holds registered VCS providers and resolves repos to providers.
type Registry struct {
	providers []Provider
}

// NewRegistry creates an empty registry.
func NewRegistry() *Registry {
	return &Registry{}
}

// Register adds a provider to the registry.
func (r *Registry) Register(p Provider) {
	r.providers = append(r.providers, p)
}

// ResolveURL tries each provider's ParseRepoFromURL and returns the first match.
func (r *Registry) ResolveURL(rawURL string) (repo string, provider Provider, ok bool) {
	for _, p := range r.providers {
		if repo, ok := p.ParseRepoFromURL(rawURL); ok {
			return repo, p, true
		}
	}
	return "", nil, false
}

// ByName looks up a provider by its Name().
func (r *Registry) ByName(name string) (Provider, bool) {
	for _, p := range r.providers {
		if p.Name() == name {
			return p, true
		}
	}
	return nil, false
}

// All returns all registered providers.
func (r *Registry) All() []Provider {
	return r.providers
}

var urlRe = regexp.MustCompile(`https?://[^\s<>\[\]()]+|[a-zA-Z0-9-]+\.[a-zA-Z]{2,}/[^\s<>\[\]()]+`)

// ExtractReposFromText finds all URLs in text and resolves them against registered providers.
func (r *Registry) ExtractReposFromText(text string) []RepoMatch {
	urls := urlRe.FindAllString(text, -1)
	seen := make(map[string]bool)
	var matches []RepoMatch
	for _, u := range urls {
		repo, provider, ok := r.ResolveURL(u)
		if !ok {
			continue
		}
		key := fmt.Sprintf("%s:%s", provider.Name(), repo)
		if seen[key] {
			continue
		}
		seen[key] = true
		matches = append(matches, RepoMatch{Repo: repo, Provider: provider})
	}
	return matches
}
