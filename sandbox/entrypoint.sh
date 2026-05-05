#!/bin/sh
if [ -n "${GITHUB_TOKEN}" ]; then
  echo "https://x-access-token:${GITHUB_TOKEN}@github.com" > ~/.git-credentials
  git config --global credential.helper store
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
