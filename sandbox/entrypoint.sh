#!/bin/sh
git config --global credential.helper '!f() { echo "username=x-access-token"; echo "password=${GITHUB_TOKEN}"; }; f'
gh auth login --with-token <<< "${GITHUB_TOKEN}" 2>/dev/null || true
exec claude --dangerously-skip-permissions -p "$@"
