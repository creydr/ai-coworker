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

Kustomize-based manifests at `deploy/kubernetes/`:

```
deploy/kubernetes/
  kustomization.yaml     # ties everything together
  namespace.yaml         # ai-coworker namespace
  serviceaccount.yaml    # SA for the service
  role.yaml              # RBAC for sandbox operations
  rolebinding.yaml       # binds role to SA
  configmap.yaml         # non-secret config (config.yaml)
  secret.yaml            # template for API keys
  deployment.yaml        # service deployment
  service.yaml           # ClusterIP for webhook delivery
```

Usage:

```sh
# Edit secret.yaml with your API keys
kubectl apply -k deploy/kubernetes/
```

The Deployment uses TCP liveness/readiness probes on port 8080. A dedicated health endpoint is a follow-up improvement.

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