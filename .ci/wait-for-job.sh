#!/usr/bin/env bash
set -Eeuo pipefail

# wait-for-job.sh - polls a GitHub Actions workflow for a specific job's completion
# on a given commit. GitHub Actions has no native way to block on a job in a
# different, independently-triggered workflow file, so this fills that gap.
#
# Usage: wait-for-job.sh <workflow-file> <job-name> <sha>
# Required env: GH_TOKEN, GITHUB_REPOSITORY
# Optional env: MAX_ATTEMPTS (default 60), POLL_INTERVAL_SECONDS (default 30)
# On success, appends "run_id=<id>" to $GITHUB_OUTPUT (if set) and exits 0.

main() {
  local workflow=$1
  local job_name=$2
  local sha=$3
  local max_attempts=${MAX_ATTEMPTS:-60}
  local poll_interval=${POLL_INTERVAL_SECONDS:-30}
  local attempt=1

  while ((attempt <= max_attempts)); do
    local run_id
    run_id=$(gh run list --repo "${GITHUB_REPOSITORY}" --workflow "${workflow}" --commit "${sha}" \
      --json databaseId --jq '.[0].databaseId // empty')

    if [[ -n "${run_id}" ]]; then
      local job_conclusion
      # shellcheck disable=SC2016 # single-quoted on purpose: $name is a jq var bound via --arg, not a shell expansion
      job_conclusion=$(gh run view "${run_id}" --repo "${GITHUB_REPOSITORY}" --json jobs |
        jq -r --arg name "${job_name}" '.jobs[] | select(.name == $name) | .conclusion // empty')

      case "${job_conclusion}" in
      success)
        echo "${job_name} succeeded (run ${run_id})"
        if [[ -n "${GITHUB_OUTPUT:-}" ]]; then
          echo "run_id=${run_id}" >>"${GITHUB_OUTPUT}"
        fi
        return 0
        ;;
      failure | cancelled)
        echo "${job_name} did not succeed: ${job_conclusion}" >&2
        return 1
        ;;
      esac
    fi

    echo "waiting for ${job_name} on ${workflow} (attempt ${attempt}/${max_attempts})..."
    sleep "${poll_interval}"
    ((attempt++))
  done

  echo "timed out waiting for ${job_name} on ${workflow}" >&2
  return 1
}

main "$@"
