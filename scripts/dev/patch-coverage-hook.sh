#!/usr/bin/env bash
# Claude Code Stop and SubagentStop hook: block the stop while newly edited Go lines lack coverage.
#
# Reads the hook JSON on stdin. Exits 0 to allow the stop, 2 (message on stderr) to block it.
# Runs `make patch-coverage` locally, or over ssh when BYOH_REMOTE_HOST is set
# (checkout dir from BYOH_REMOTE_DIR, default ~/byoh on the remote host).
#
# Environment:
#   PATCH_COVERAGE_BASE     base ref, default origin/main (the Makefile reads the same name)
#   PATCH_COVERAGE_PROFILE  coverage profile, read by the Makefile
#   PATCH_COVERAGE_OUT_DIR  where the report and the snapshot live, default _artifacts
#
# The hook acts only on lines it has not reported before. Every missed or partial line in
# patch-coverage.json is tracked as "file:line". When the check finds gaps, the hook
# compares them with the snapshot of the previous check (patch-coverage-reported.json),
# then overwrites the snapshot with the current set:
#   - at least one line is not in the old snapshot: block (exit 2), list only the new lines
#   - every line is in the old snapshot: exit 0 silently
# The snapshot always mirrors the last check, so a line that drops out and comes back counts
# as new. A passing check deletes the snapshot. An edit that shifts line numbers makes the
# shifted lines count as new; that is accepted.
# Errors from the tool (stale profile, missing cover.out, missing jq) leave no report and
# always block.
set -Eeuo pipefail
shopt -s nullglob

BASE=${PATCH_COVERAGE_BASE:-origin/main}
OUT_DIR=${PATCH_COVERAGE_OUT_DIR:-_artifacts}
REPORT=$OUT_DIR/patch-coverage.json
SNAPSHOT=$OUT_DIR/patch-coverage-reported.json

log() { printf '%s %s\n' "$(date -u +%FT%TZ)" "$*" >&2; }

# Print non-test, non-generated Go files that changed against the merge base.
changed_source_files() {
  local merge_base=$1
  {
    git diff --name-only "$merge_base" -- '*.go' ':!*_test.go' ':!*zz_generated*'
    git ls-files --others --exclude-standard -- '*.go' ':!*_test.go' ':!*zz_generated*'
  } | sort -u
}

# Remove the old report, then run the check. A report that exists afterwards means
# the tool ran to the end and found gaps; no report means the tool failed.
run_patch_coverage() {
  if [[ -n ${BYOH_REMOTE_HOST:-} ]]; then
    # shellcheck disable=SC2029 # the variables are meant to expand on this side
    ssh "$BYOH_REMOTE_HOST" "cd ${BYOH_REMOTE_DIR:-~/byoh} && rm -f '$REPORT' && PATCH_COVERAGE_OUT_DIR='$OUT_DIR' make patch-coverage"
  else
    rm -f "$REPORT"
    make patch-coverage
  fi
}

# Print the report JSON, from the remote checkout when BYOH_REMOTE_HOST is set.
read_report() {
  if [[ -n ${BYOH_REMOTE_HOST:-} ]]; then
    # shellcheck disable=SC2029 # the variables are meant to expand on this side
    ssh "$BYOH_REMOTE_HOST" "cd ${BYOH_REMOTE_DIR:-~/byoh} && cat '$REPORT'"
  else
    cat "$REPORT"
  fi
}

# Read a report on stdin, print the sorted JSON array of "file:line" for every missed or partial line.
gap_lines() {
  jq -c '
    [.files[] | .file as $f | (.missed + .partial)[] | range(.[0]; .[1] + 1) | "\($f):\(.)"]
    | unique'
}

# Read a JSON array of "file:line" on stdin, print "file: 3,5-7" per file.
format_lines() {
  jq -r '
    def ranges:
      reduce .[] as $l ([];
        if length > 0 and .[-1][1] == $l - 1 then .[-1][1] = $l else . + [[$l, $l]] end)
      | map(if .[0] == .[1] then "\(.[0])" else "\(.[0])-\(.[1])" end)
      | join(",");
    map(capture("^(?<f>.+):(?<l>[0-9]+)$") | {f, l: (.l | tonumber)})
    | group_by(.f)[]
    | "\(.[0].f): \(map(.l) | sort | ranges)"'
}

block() {
  {
    printf '%s\n\n' "$1"
    printf 'Add tests for each line, re-run the make test target, and re-check with make patch-coverage.\n'
    printf 'If a line is genuinely unreachable, justify each such line in your report.\n'
  } >&2
  exit 2
}

main() {
  local input active root merge_base changed output status current previous new_lines

  input=$(cat)
  active=$(jq -r '.stop_hook_active // false' <<<"$input")
  if [[ $active == true ]]; then
    exit 0
  fi

  root=$(git rev-parse --show-toplevel) || exit 0
  cd "$root"
  merge_base=$(git merge-base "$BASE" HEAD 2>/dev/null) || {
    log "patch-coverage hook: cannot find merge base with $BASE, skipping"
    exit 0
  }
  changed=$(changed_source_files "$merge_base")
  if [[ -z $changed ]]; then
    exit 0
  fi

  log "patch-coverage hook: running make patch-coverage"
  status=0
  output=$(run_patch_coverage 2>&1) || status=$?
  if ((status == 0)); then
    rm -f "$SNAPSHOT"
    exit 0
  fi

  if ! current=$(read_report 2>/dev/null | gap_lines) || [[ -z $current ]]; then
    block "make patch-coverage failed (exit $status) without writing a report:

$output"
  fi

  previous=$(cat "$SNAPSHOT" 2>/dev/null || echo '[]')
  new_lines=$(jq -c --argjson prev "$previous" '. - $prev' <<<"$current")
  mkdir -p "$OUT_DIR"
  printf '%s\n' "$current" >"$SNAPSHOT"
  if [[ $new_lines == '[]' ]]; then
    exit 0
  fi

  block "Changed lines without full test coverage, not reported before:

$(format_lines <<<"$new_lines")"
}

if [[ ${BASH_SOURCE[0]} == "$0" ]]; then
  main "$@"
fi
