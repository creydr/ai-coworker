# Kubernetes Support — Design Document

## Problem

The sandbox runtime currently only supports Docker. To run AI Coworker in production on Kubernetes, we need:

1. A Kubernetes sandbox runtime that executes tasks as Jobs instead of Docker containers.
2. Kubernetes deployment manifests for the service itself.

## Goals

- Add Kubernetes as a sandbox runtime option alongside Docker, selectable via config.
- Use Kubernetes Jobs for sandbox execution (one-shot, no retries, automatic cleanup).
- Provide ready-to-use Kustomize manifests for deploying the service on Kubernetes.
- Keep Docker as the default for local development — no breaking changes.

## Non-Goals

- Helm charts (Kustomize is sufficient for now).
- Multi-cluster support.
- Custom scheduling or node affinity for sandbox Jobs.

## Design

### Sandbox Runtime Selection

The `sandbox.Runtime` interface is already pluggable — a single `Exec(ctx, ExecRequest) (*ExecResult, error)` method. The config already has `sandbox.runtime` and `sandbox.namespace` fields that are currently unused.

```
sandbox:
  runtime: "kubernetes"        # "docker" (default) or "kubernetes"
  namespace: "ai-coworker"     # required for kubernetes runtime
  service_account: ""          # optional SA for sandbox Job pods
  image: "quay.io/creydr/ai-coworker-sandbox:latest"
  timeout_seconds: 600
  cpu_limit: "2"
  memory_limit: "2Gi"
```

Runtime selection happens in `main.go`:

```
cfg.Sandbox.Runtime == "kubernetes"  →  kubernetes.New(namespace, serviceAccount)
cfg.Sandbox.Runtime == "docker" / "" →  docker.New()
```

The Kubernetes runtime always uses in-cluster config — it only runs when the service is deployed on a cluster.

### Kubernetes Sandbox Execution Flow

The K8s runtime mirrors the Docker runtime's behavior:

```
1. Generate unique name (sandbox-<uuid>)
2. Create ConfigMap with prompt content
3. Create Job:
   - Image, env vars, resource limits from ExecRequest
   - ConfigMap volume mounted at /tmp/prompt.txt (matches entrypoint.sh)
   - BackoffLimit: 0, RestartPolicy: Never
   - ttlSecondsAfterFinished: 3600 (safety net)
   - Label: app.kubernetes.io/managed-by: ai-coworker
4. Watch Job for completion
5. Read Pod logs (stdout → ExecResult.Output)
6. Cleanup: delete Job + ConfigMap
```

#### Prompt Delivery

The Docker runtime writes the prompt to a temp file and bind-mounts it at `/tmp/prompt.txt`. The Kubernetes runtime uses a ConfigMap instead:

```yaml
volumes:
  - name: prompt
    configMap:
      name: sandbox-<uuid>
volumeMounts:
  - name: prompt
    mountPath: /tmp/prompt.txt
    subPath: prompt.txt
    readOnly: true
```

This is transparent to `entrypoint.sh` which reads `cat /tmp/prompt.txt | claude ...`.

ConfigMap data values have a 1MB limit (etcd). Prompts are far smaller in practice, but the runtime should check and return a clear error if exceeded.

#### Resource Limits

The existing `CPULimit` format (`"2"`) and `MemLimit` format (`"2Gi"`) are already valid Kubernetes resource quantity strings. The Docker runtime has to parse these manually, but the K8s runtime can pass them directly to `resource.MustParse()`.

#### Cleanup

Deferred cleanup deletes both the Job (with background propagation to GC the Pod) and the ConfigMap. The `ttlSecondsAfterFinished` field on the Job acts as a safety net if the service crashes mid-execution. The `app.kubernetes.io/managed-by: ai-coworker` label enables manual cleanup of orphaned resources.

### RBAC Requirements

The service's ServiceAccount needs these permissions in the sandbox namespace:

| API Group | Resource | Verbs |
|-----------|----------|-------|
| `batch` | `jobs` | create, delete, get, list, watch |
| `""` | `configmaps` | create, delete, get |
| `""` | `pods` | get, list |
| `""` | `pods/log` | get |

### Deployment Manifests

Kustomize-based manifests at `deploy/kubernetes/`, using [Kustomize components](https://kubectl.docs.kubernetes.io/guides/config_management/components/) for composability:

```
deploy/kubernetes/
  base/                            # core manifests
    kustomization.yaml
    namespace.yaml
    serviceaccount.yaml
    role.yaml / rolebinding.yaml
    configmap.yaml / secret.yaml
    deployment.yaml / service.yaml
  components/
    postgres/                      # PostgreSQL Deployment + Service
    google-adc/                    # Google ADC secret + volume mount for Vertex AI
  overlays/
    with-postgres/                 # base + postgres (bring-your-own LLM credentials)
    kind/                          # base + postgres + google-adc (local KinD testing)
```

Users compose what they need in their own overlay. The `components/` approach avoids a combinatorial explosion of overlays.

The Deployment uses a startup probe (65s budget) to allow database connection time, followed by TCP liveness/readiness probes on port 8080. A dedicated health endpoint is a follow-up improvement.

### Podman Compatibility

The existing Docker runtime already works with Podman via its Docker-compatible API. Set `DOCKER_HOST=unix:///run/user/$UID/podman/podman.sock` after enabling the Podman socket (`systemctl --user start podman.socket`). No code changes needed.

## Implementation Plan

Two PRs:

**PR 1 — Kubernetes Sandbox Runtime:**
1. Add `k8s.io/client-go`, `k8s.io/api`, `k8s.io/apimachinery` dependencies
2. Add `ServiceAccount` field to `SandboxConfig`, add runtime validation
3. Create `internal/sandbox/kubernetes/kubernetes.go`
4. Create `internal/sandbox/kubernetes/kubernetes_test.go`
5. Wire runtime selection in `cmd/ai-coworker/main.go`
6. Update docs (README, AGENTS.md, architecture.md, config.yaml)

**PR 2 — Kubernetes Deployment Manifests:**
1. Create all manifests in `deploy/kubernetes/`
2. Update README with deployment instructions

**PR 3 — Local KinD E2E Testing:**
1. Add Makefile targets for KinD lifecycle and testing
2. Document the testing workflow

## Local Testing with KinD

For end-to-end testing of the Kubernetes deployment, we use [KinD](https://kind.sigs.k8s.io/) (Kubernetes in Docker) to spin up a local cluster.

### What It Tests

- Kustomize manifests apply cleanly (base + components)
- PostgreSQL starts and becomes ready
- ai-coworker Deployment starts, connects to PostgreSQL, runs migrations
- RBAC allows the service to create sandbox Jobs
- GitHub webhooks reach the service via smee → port-forward
- Sandbox Jobs execute and produce output

### How It Works

1. **KinD cluster** — a lightweight single-node K8s cluster running in Docker
2. **Local images** — both `ai-coworker` and `ai-coworker-sandbox` are built locally and loaded into KinD (no registry push needed). Images are tagged with the full `quay.io/creydr/` prefix so the existing kustomization image references work without overrides.
3. **KinD overlay** — uses `deploy/kubernetes/overlays/kind/` which composes the `postgres` and `google-adc` components
4. **Secrets** — all `AI_COWORKER__*` env vars are collected into a K8s secret at deploy time (multiline values like PEM keys are preserved via temp files). Google ADC credentials are populated from the local `~/.config/gcloud/application_default_credentials.json`.
5. **Webhook routing** — `kubectl port-forward` exposes the service on `localhost:8080`, and `smee` proxies GitHub webhooks to it

### hack/kind.sh

A single script (`hack/kind.sh`) handles the full KinD lifecycle. Commands use `--` flags and can be combined — they always execute in the correct lifecycle order regardless of argument order:

```sh
./hack/kind.sh --create --load --deploy    # runs create, then load, then deploy
./hack/kind.sh --deploy --create           # same order: create first, then deploy
```

Makefile targets are thin wrappers around the script.

| Target / Flag | Description |
|---------------|-------------|
| `make kind-create` / `--create` | Create a KinD cluster named `ai-coworker` |
| `make kind-load` / `--load` | Build both images and load them into the KinD cluster |
| `make kind-deploy` / `--deploy` | Deploy the kind overlay, inject secrets from env vars, wait for rollout |
| `make kind-smee` / `--smee` | Port-forward the service and start smee for GitHub webhook delivery |
| `make kind-delete` / `--delete` | Tear down the KinD cluster |

### Environment Variables

All `AI_COWORKER__*` env vars set in your shell are automatically injected into the K8s secret. Common ones:

| Variable | Purpose |
|----------|---------|
| `AI_COWORKER__LLM__PROVIDER` | LLM provider (`vertex`, `claude`, `openai`) |
| `AI_COWORKER__LLM__VERTEX__PROJECT_ID` | Google Cloud project ID (for Vertex AI) |
| `AI_COWORKER__LLM__API_KEY` | API key (for Claude or OpenAI providers) |
| `AI_COWORKER__GITHUB__*` | GitHub App credentials (APP_ID, PRIVATE_KEY, WEBHOOK_SECRET, BOT_USERNAME) |
| `SMEE_URL` | smee.io channel URL (required for `--smee`) |

### Typical Workflow

```sh
make kind-create                          # create cluster
make kind-load                            # build & load images
AI_COWORKER__LLM__PROVIDER=vertex \
AI_COWORKER__LLM__VERTEX__PROJECT_ID=... \
AI_COWORKER__GITHUB__ENABLED=true \
AI_COWORKER__GITHUB__APP_ID=... \
AI_COWORKER__GITHUB__PRIVATE_KEY="$(cat key.pem)" \
AI_COWORKER__GITHUB__WEBHOOK_SECRET="$(cat secret)" \
AI_COWORKER__GITHUB__BOT_USERNAME=my-bot \
  make kind-deploy                        # deploy + inject secrets
SMEE_URL=https://smee.io/... make kind-smee  # forward webhooks
# ... test by commenting on a GitHub issue ...
make kind-delete                          # tear down
```
