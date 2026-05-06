# Extract VCS Provider Abstraction

## Context

The executor layer (`claudecode.go`) is tightly coupled to GitHub: it has a `GitHubTokenFunc`, hardcodes `https://github.com/` clone URLs, and the entrypoint hardcodes `github.com` in git credentials. This prevents Slack-originated tasks from working with repos (no token, no clone URL) and makes adding GitLab or other VCS platforms impossible without modifying the executor.

The goal is to introduce a VCS provider interface that abstracts token creation, clone URLs, and credential setup — so the executor is platform-agnostic and any adapter can trigger code tasks on any VCS platform.

**Key principles**:
- A sandbox session may need tokens for multiple VCS providers simultaneously
- Repo extraction from messages is deterministic URL parsing (no LLM), done in the executor
- If a repo reference is ambiguous (no URL, just a name) and the source platform is unknown, the bot asks the user — no guessing
- If the event came from a VCS adapter (e.g. GitHub), bare repo names are assumed to be on the same platform

## Step 1: Create VCS provider interface and registry

**New file: `internal/vcs/vcs.go`**

```go
type Provider interface {
    Name() string
    CreateTokenForRepo(ctx context.Context, repo string) (string, error)
    CloneURL(repo string) string
    TokenEnvVar() string               // e.g. "GITHUB_TOKEN", "GITLAB_TOKEN"
    CredentialURL(token string) string  // e.g. "https://x-access-token:<token>@github.com"
    ParseRepoFromURL(url string) (repo string, ok bool)  // extract "owner/repo" from a URL if hostname matches
}

type Registry struct {
    providers []Provider
}

func NewRegistry() *Registry
func (r *Registry) Register(p Provider)
func (r *Registry) ResolveURL(url string) (repo string, provider Provider, ok bool)  // find provider by URL hostname
func (r *Registry) ByName(name string) (Provider, bool)  // look up provider by name (e.g. "github")
func (r *Registry) All() []Provider
func (r *Registry) ExtractReposFromText(text string) []RepoMatch  // find all repo URLs in text
```

```go
type RepoMatch struct {
    Repo     string    // "owner/repo"
    Provider Provider  // the provider that owns it
}
```

- `ResolveURL` tries each provider's `ParseRepoFromURL`; returns the first match by hostname — no ambiguity since each provider owns distinct hostnames
- `ByName` looks up a provider by its `Name()` (e.g. `"github"`), used to resolve bare repo names against the source platform
- `ExtractReposFromText` finds all URLs in text via regex, then calls `ResolveURL` on each
- `TokenEnvVar` returns the provider-specific env var name so multiple tokens can coexist
- `CredentialURL` returns the git-credential-store line for that provider's host

No `CanHandle(repo)` method — we don't guess which provider owns an unqualified repo name. Only URL-based matching or explicit source platform context.

**New file: `internal/vcs/vcs_test.go`** — test registry resolution, `ExtractReposFromText` with mixed URLs, no-match cases

## Step 2: Create GitHub VCS provider

**New file: `internal/vcs/github/github.go`**

Extracts VCS operations from `internal/adapter/github/github.go`:

```go
type Provider struct {
    appsTransport     *ghinstallation.AppsTransport
    repoInstallations sync.Map  // "owner/repo" → int64 installationID
}

func New(appsTransport *ghinstallation.AppsTransport) *Provider
func (p *Provider) Name() string                                          // "github"
func (p *Provider) ParseRepoFromURL(url string) (repo string, ok bool)    // matches github.com host
func (p *Provider) CreateTokenForRepo(ctx, repo) (string, error)          // scoped installation token
func (p *Provider) CloneURL(repo) string                                  // "https://github.com/<repo>.git"
func (p *Provider) TokenEnvVar() string                                   // "GITHUB_TOKEN"
func (p *Provider) CredentialURL(token) string                            // "https://x-access-token:<token>@github.com"
func (p *Provider) TrackInstallation(repo string, installationID int64)   // called by GitHub adapter on webhooks
func (p *Provider) GetInstallationID(ctx, repo) (int64, error)            // used by adapter for API clients
```

`ParseRepoFromURL` checks if the URL hostname is `github.com` and extracts `owner/repo` from the path. Handles URLs like:
- `https://github.com/org/repo`
- `https://github.com/org/repo/pull/42`
- `https://github.com/org/repo/issues/7`
- `github.com/org/repo` (no scheme)

`CreateTokenForRepo`: checks cached `repoInstallations` first. If not found, calls `Apps.FindRepositoryInstallation` API to discover the installation ID, caches it, then creates a scoped token. This solves the "unknown repo from Slack" problem.

**New file: `internal/vcs/github/github_test.go`** — test `ParseRepoFromURL` (various URL formats, non-matching hosts), `CloneURL`, `TokenEnvVar`, `CredentialURL`, `splitRepo`

## Step 3: Refactor GitHub adapter to use VCS provider

**Modified: `internal/adapter/github/github.go`**

- Add `vcsProvider *vcsgithub.Provider` field to `Adapter` struct
- `New()` creates `vcsgithub.Provider` wrapping the `AppsTransport`
- Webhook handlers call `a.vcsProvider.TrackInstallation()` instead of `a.repoInstallations.Store()`
- Webhook handlers set `Metadata["vcs"] = "github"` on all `IncomingEvent`s — this tells the executor which platform the event came from, so bare repo names in the message can be resolved against the same provider
- `getClientForRepo` uses `a.vcsProvider.GetInstallationID()` instead of own `repoInstallations` map
- Remove `CreateInstallationTokenForRepo` (moved to VCS provider)
- Remove adapter's own `repoInstallations` sync.Map (VCS provider owns it)
- Keep `installationClients` sync.Map (caches API clients, adapter-internal concern)
- Add `VCSProvider() vcs.Provider` accessor for `main.go` to register

**Modified: `internal/adapter/github/github_test.go`** — update tests for new wiring, verify `Metadata["vcs"]` is set

## Step 4: Refactor executor to use VCS registry

**Modified: `internal/executor/claudecode/claudecode.go`**

- Replace `GitHubTokenFunc` with `VCSRegistry *vcs.Registry` in `Config` and `Executor`
- Add repo extraction with three-tier resolution
- Replace hardcoded clone URL with `provider.CloneURL(repo)`
- Replace token acquisition — for each matched provider, create token and set env vars

**Repo resolution logic (three tiers):**

```go
// Tier 1: Metadata from VCS adapter (GitHub webhook events already set "repo" + "vcs")
repo := execCtx.Event.Metadata["repo"]
sourceVCS := execCtx.Event.Metadata["vcs"]  // e.g. "github" — set by VCS adapters
var primaryProvider vcs.Provider

if repo != "" && sourceVCS != "" {
    // Event came from a VCS adapter — provider is known
    primaryProvider, _ = e.vcsRegistry.ByName(sourceVCS)
}

// Tier 2: Extract repo URLs from message content + thread history
var allMatches []vcs.RepoMatch
allMatches = append(allMatches, e.vcsRegistry.ExtractReposFromText(execCtx.Task.Input)...)
for _, msg := range execCtx.Messages {
    allMatches = append(allMatches, e.vcsRegistry.ExtractReposFromText(msg.Content)...)
}

if repo == "" && len(allMatches) > 0 {
    // No metadata repo — use first URL match as primary
    repo = allMatches[0].Repo
    primaryProvider = allMatches[0].Provider
}

// Tier 3: If still no repo, cannot proceed with code task
// (the worker should detect this and ask the user via the originating adapter)

// Build clone URL from the primary provider
if primaryProvider != nil && repo != "" {
    cloneURL = primaryProvider.CloneURL(repo)
}

// Collect tokens for all involved providers (primary + any others from URLs)
seen := map[string]bool{}
var credURLs []string
// Always include primary provider
if primaryProvider != nil && repo != "" {
    token, err := primaryProvider.CreateTokenForRepo(ctx, repo)
    if err == nil {
        envVars[primaryProvider.TokenEnvVar()] = token
        credURLs = append(credURLs, primaryProvider.CredentialURL(token))
        seen[primaryProvider.Name()] = true
    }
}
// Add tokens for any additional providers found in URLs
for _, match := range allMatches {
    if seen[match.Provider.Name()] { continue }
    seen[match.Provider.Name()] = true
    token, err := match.Provider.CreateTokenForRepo(ctx, match.Repo)
    if err == nil {
        envVars[match.Provider.TokenEnvVar()] = token
        credURLs = append(credURLs, match.Provider.CredentialURL(token))
    }
}
if len(credURLs) > 0 {
    envVars["VCS_CREDENTIAL_URLS"] = strings.Join(credURLs, "\n")
}
```

**Bare repo names** (e.g. "use the same Go version as in repo2"): The executor does not need to pre-resolve these. The sandbox already has the VCS token and CLI tools (`gh`, etc.) — Claude Code can look up bare repo names at execution time via `gh repo view repo2` or API calls. The `Metadata["vcs"]` tells the executor which provider the event came from, so it passes the correct token. Claude Code handles the rest inside the sandbox.

**Modified: `internal/executor/claudecode/claudecode_test.go`** — replace `GitHubTokenFunc` tests with `VCSRegistry` tests using mock provider

## Step 5: Update sandbox entrypoint

**Modified: `sandbox/entrypoint.sh`**

- Use `VCS_CREDENTIAL_URLS` (newline-separated) to write multiple credential lines to `~/.git-credentials`
- Keep `gh auth login` via `GITHUB_TOKEN` (only when present)
- Keep backward compatibility: if `VCS_CREDENTIAL_URLS` is not set but `GITHUB_TOKEN` is, fall back to current behavior

```sh
if [ -n "${VCS_CREDENTIAL_URLS}" ]; then
  printf '%s\n' "${VCS_CREDENTIAL_URLS}" > ~/.git-credentials
  git config --global credential.helper store
elif [ -n "${GITHUB_TOKEN}" ]; then
  echo "https://x-access-token:${GITHUB_TOKEN}@github.com" > ~/.git-credentials
  git config --global credential.helper store
fi
if [ -n "${GITHUB_TOKEN}" ]; then
  echo "${GITHUB_TOKEN}" | gh auth login --with-token 2>/dev/null || true
fi
```

## Step 6: Wire VCS registry in main.go

**Modified: `cmd/ai-coworker/main.go`**

```go
// Replace:
var githubTokenFunc func(ctx context.Context, repo string) (string, error)
if githubAdapter != nil {
    githubTokenFunc = githubAdapter.CreateInstallationTokenForRepo
}
codeExec := claudecode.New(claudecode.Config{
    ...
    GitHubTokenFunc: githubTokenFunc,
})

// With:
vcsRegistry := vcs.NewRegistry()
if githubAdapter != nil {
    vcsRegistry.Register(githubAdapter.VCSProvider())
}
// Future: if gitlabAdapter != nil { vcsRegistry.Register(gitlabAdapter.VCSProvider()) }
codeExec := claudecode.New(claudecode.Config{
    ...
    VCSRegistry: vcsRegistry,
})
```

## Files changed

| File | Action |
|------|--------|
| `internal/vcs/vcs.go` | Create — Provider interface + Registry with URL extraction |
| `internal/vcs/vcs_test.go` | Create — Registry and extraction tests |
| `internal/vcs/github/github.go` | Create — GitHub VCS provider with URL parsing + installation discovery |
| `internal/vcs/github/github_test.go` | Create — GitHub provider tests |
| `internal/adapter/github/github.go` | Modify — delegate VCS to provider, remove `CreateInstallationTokenForRepo` |
| `internal/adapter/github/github_test.go` | Modify — update for new wiring |
| `internal/executor/claudecode/claudecode.go` | Modify — `VCSRegistry` replaces `GitHubTokenFunc`, add repo extraction from text |
| `internal/executor/claudecode/claudecode_test.go` | Modify — mock VCS provider tests |
| `sandbox/entrypoint.sh` | Modify — generic multi-provider credentials |
| `cmd/ai-coworker/main.go` | Modify — create registry, register providers |

## What this does NOT change

- **Adapter interface** (`adapter.Adapter`) — stays the same, VCS is a separate concern
- **Intent classifier** — GitHub-specific short-circuits remain (harmlessly inert for non-GitHub events)
- **Database schema** — no changes
- **Config schema** — no changes
- **Self-hosted VCS instances** — deferred; the design accommodates it (providers can be configured with custom hostnames later)

## Verification

1. `go test ./internal/vcs/...` — new package tests pass
2. `go test ./internal/adapter/github/...` — adapter tests pass with VCS provider delegation
3. `go test ./internal/executor/claudecode/...` — executor tests pass with VCS registry
4. `go test ./...` — full test suite passes
5. End-to-end: trigger a code task from GitHub, verify sandbox gets `GITHUB_TOKEN` and `VCS_CREDENTIAL_URLS`, clones repo, executes successfully
6. End-to-end: trigger a code task from Slack with a GitHub URL, verify repo is extracted and sandbox works