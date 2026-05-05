#!/usr/bin/env bash
set -euo pipefail

CLUSTER_NAME="${KIND_CLUSTER_NAME:-ai-coworker}"
NAMESPACE="${KIND_NAMESPACE:-ai-coworker}"
IMAGE="${IMAGE:-quay.io/creydr/ai-coworker:latest}"
SANDBOX_IMAGE="${SANDBOX_IMAGE:-quay.io/creydr/ai-coworker-sandbox:latest}"

cmd_create() {
  kind create cluster --name "${CLUSTER_NAME}"
}

cmd_load() {
  kind load docker-image "${IMAGE}" "${SANDBOX_IMAGE}" --name "${CLUSTER_NAME}"
}

cmd_deploy() {
  kubectl apply -k deploy/kubernetes/overlays/kind/

  # Build secret from all AI_COWORKER__* env vars using a temp env file
  # to preserve multiline values (e.g. PEM keys)
  local tmpdir
  tmpdir=$(mktemp -d)
  trap "rm -rf ${tmpdir}" RETURN

  local -a secret_args=()
  secret_args+=(--from-literal=AI_COWORKER__DATABASE__URL="postgres://ai-coworker:password@postgres:5432/ai-coworker?sslmode=disable")

  while IFS= read -r key; do
    printf '%s' "${!key}" > "${tmpdir}/${key}"
    secret_args+=(--from-file="${key}=${tmpdir}/${key}")
  done < <(compgen -v | grep '^AI_COWORKER__' | sort)

  kubectl -n "${NAMESPACE}" create secret generic ai-coworker \
    "${secret_args[@]}" \
    --dry-run=client -o yaml | kubectl apply -f -

  # Populate google-adc secret from local ADC file
  local adc_file="${GOOGLE_APPLICATION_CREDENTIALS:-${HOME}/.config/gcloud/application_default_credentials.json}"
  if [[ -f "${adc_file}" ]]; then
    kubectl -n "${NAMESPACE}" create secret generic google-adc \
      --from-file=adc.json="${adc_file}" \
      --dry-run=client -o yaml | kubectl apply -f -
  fi

  kubectl -n "${NAMESPACE}" rollout restart deployment/ai-coworker
  kubectl -n "${NAMESPACE}" rollout status deployment/ai-coworker --timeout=120s
}

cmd_smee() {
  if [[ -z "${SMEE_URL:-}" ]]; then
    echo "Error: SMEE_URL is required" >&2
    exit 1
  fi
  kubectl -n "${NAMESPACE}" port-forward svc/ai-coworker 8080:8080 &
  smee -u "${SMEE_URL}" -t http://localhost:8080/webhook/github
}

cmd_delete() {
  kind delete cluster --name "${CLUSTER_NAME}"
}

usage() {
  cat <<EOF
Usage: $(basename "$0") [--create] [--load] [--deploy] [--smee] [--delete]

Multiple commands can be combined, e.g.: $(basename "$0") --create --load --deploy

Commands:
  --create   Create a KinD cluster
  --load     Load images into the KinD cluster
  --deploy   Deploy the service with PostgreSQL overlay
  --smee     Port-forward and start smee for GitHub webhooks
  --delete   Delete the KinD cluster

Environment variables:
  KIND_CLUSTER_NAME   Cluster name (default: ai-coworker)
  KIND_NAMESPACE      Namespace (default: ai-coworker)
  IMAGE               Service image tag (default: quay.io/creydr/ai-coworker:latest)
  SANDBOX_IMAGE       Sandbox image tag (default: quay.io/creydr/ai-coworker-sandbox:latest)
  SMEE_URL            smee.io channel URL (required for --smee)
  AI_COWORKER__*      All env vars with this prefix are injected into the K8s secret
                      (used by --deploy, supports multiline values like PEM keys)
EOF
}

DO_CREATE=false
DO_LOAD=false
DO_DEPLOY=false
DO_SMEE=false
DO_DELETE=false

if [[ $# -eq 0 ]]; then
  usage
  exit 1
fi

while [[ $# -gt 0 ]]; do
  case "$1" in
    --create) DO_CREATE=true ;;
    --load)   DO_LOAD=true ;;
    --deploy) DO_DEPLOY=true ;;
    --smee)   DO_SMEE=true ;;
    --delete) DO_DELETE=true ;;
    --help|-h) usage; exit 0 ;;
    *) echo "Unknown option: $1" >&2; usage; exit 1 ;;
  esac
  shift
done

# Execute in correct lifecycle order
${DO_CREATE} && cmd_create
${DO_LOAD}   && cmd_load
${DO_DEPLOY} && cmd_deploy
${DO_SMEE}   && cmd_smee
${DO_DELETE}  && cmd_delete

exit 0
