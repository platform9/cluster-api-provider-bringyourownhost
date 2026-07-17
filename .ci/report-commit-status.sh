#!/usr/bin/env bash
set -Eeuo pipefail

# report-commit-status.sh - posts a commit status against a given SHA via the GitHub API.
#
# workflow_run-triggered jobs don't automatically surface as a check on the PR that triggered
# the upstream workflow, so downstream workflows call this explicitly (once as "pending" when
# they start, once as "success"/"failure" when they finish) to report their own status back
# onto that commit.
#
# Usage: report-commit-status.sh <state> <context> <description>
# Required env: GH_TOKEN, SHA (the commit to report against)
# Relies on GitHub Actions' own GITHUB_REPOSITORY, GITHUB_SERVER_URL, GITHUB_RUN_ID.

main() {
  local state=$1
  local context=$2
  local description=$3

  gh api "repos/${GITHUB_REPOSITORY}/statuses/${SHA}" \
    --method POST \
    -f state="${state}" \
    -f context="${context}" \
    -f description="${description}" \
    -f target_url="${GITHUB_SERVER_URL}/${GITHUB_REPOSITORY}/actions/runs/${GITHUB_RUN_ID}"
}

main "$@"
