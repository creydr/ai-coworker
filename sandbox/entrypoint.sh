#!/bin/sh
if [ -n "${VCS_CREDENTIAL_URLS}" ]; then
  printf '%s\n' "${VCS_CREDENTIAL_URLS}" > ~/.git-credentials
  git config --global credential.helper store
fi
if [ -n "${GITHUB_TOKEN}" ]; then
  echo "${GITHUB_TOKEN}" | gh auth login --with-token 2>/dev/null || true
fi
if [ -n "${GOOGLE_APPLICATION_CREDENTIALS_JSON}" ]; then
  echo "${GOOGLE_APPLICATION_CREDENTIALS_JSON}" > /tmp/gcloud-adc.json
  export GOOGLE_APPLICATION_CREDENTIALS=/tmp/gcloud-adc.json
fi
if [ -n "${CLONE_URL}" ]; then
  git clone ${CLONE_BRANCH:+-b "$CLONE_BRANCH"} -- "$CLONE_URL" /workspace/repo
  cd /workspace/repo
fi
cat /tmp/prompt.txt | claude --dangerously-skip-permissions -p -
