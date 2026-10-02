#!/usr/bin/env bash
# Report which changed Go lines lack test coverage.
#
# Usage: patch-coverage.sh [BASE_REF] [COVERAGE_PROFILE]
#   BASE_REF          default origin/main
#   COVERAGE_PROFILE  default cover.out (written by every make *-test target)
#
# Writes patch-coverage.json to $PATCH_COVERAGE_OUT_DIR (default _artifacts) and prints a short summary.
# Exit codes: 0 nothing missed or partial, 1 missed or partial lines, 2 usage or input error.
#
# A changed line with statements is "hit" when every coverage block over it has
# hits, "missed" when none does, and "partial" otherwise. Test files and
# zz_generated* files are ignored. Needs bash, git and jq. Works on Linux and macOS.
set -Eeuo pipefail
shopt -s nullglob

OUT_DIR=${PATCH_COVERAGE_OUT_DIR:-_artifacts}
OUT_FILE=$OUT_DIR/patch-coverage.json

log() { printf '%s %s\n' "$(date -u +%FT%TZ)" "$*" >&2; }

die() {
  log "error: $*"
  exit 2
}

# Print the mtime of a file in epoch seconds (GNU stat first, BSD stat second).
file_mtime() {
  stat -c %Y "$1" 2>/dev/null || stat -f %m "$1"
}

# Print "file<TAB>start<TAB>end" for every added or changed line range.
changed_line_ranges() {
  local merge_base=$1
  local line file="" start count untracked n

  while IFS= read -r line; do
    if [[ $line == '+++ '* ]]; then
      file=${line#+++ }
      file=${file#b/}
      [[ $file == /dev/null ]] && file=""
    elif [[ $line =~ ^@@\ -[0-9,]+\ \+([0-9]+)(,([0-9]+))?\ @@ ]]; then
      start=${BASH_REMATCH[1]}
      count=${BASH_REMATCH[3]:-1}
      if [[ -n $file && $count -gt 0 ]]; then
        printf '%s\t%s\t%s\n' "$file" "$start" "$((start + count - 1))"
      fi
    fi
  done < <(git diff -U0 --no-color "$merge_base" -- '*.go' ':!*_test.go' ':!*zz_generated*')

  while IFS= read -r untracked; do
    n=$(grep -c '' "$untracked" || true)
    if [[ $n -gt 0 ]]; then
      printf '%s\t%s\t%s\n' "$untracked" 1 "$n"
    fi
  done < <(git ls-files --others --exclude-standard -- '*.go' ':!*_test.go' ':!*zz_generated*')
}

# Print every changed .go file that still exists, tests included.
changed_go_files() {
  local merge_base=$1
  local f
  {
    git diff --name-only "$merge_base" -- '*.go'
    git ls-files --others --exclude-standard -- '*.go'
  } | while IFS= read -r f; do
    [[ -f $f ]] && printf '%s\n' "$f"
  done
}

# Exit 2 when the profile is older than the newest changed .go file.
check_profile_fresh() {
  local merge_base=$1 profile=$2
  local profile_mtime newest=0 mtime f
  profile_mtime=$(file_mtime "$profile")
  while IFS= read -r f; do
    mtime=$(file_mtime "$f")
    if ((mtime > newest)); then
      newest=$mtime
    fi
  done < <(changed_go_files "$merge_base")
  if ((newest > profile_mtime)); then
    die "$profile is stale, re-run the make test target"
  fi
}

# Print "module path<TAB>repo dir" for every go.mod in the repo.
module_map() {
  local gomod kw rest dir
  while IFS= read -r gomod; do
    dir=$(dirname "$gomod")
    while read -r kw rest; do
      if [[ $kw == module ]]; then
        printf '%s\t%s\n' "$rest" "$dir"
        break
      fi
    done <"$gomod"
  done < <(git ls-files -- go.mod '*/go.mod')
}

# Read the profile on stdin and the changed ranges, write the JSON report.
build_report() {
  local base=$1 profile=$2 modules_json=$3 changes_json=$4
  jq -nR \
    --arg base "$base" \
    --arg profile "$profile" \
    --slurpfile mods "$modules_json" \
    --slurpfile changes "$changes_json" '
    def ranges:
      reduce .[] as $l ([];
        if length > 0 and .[-1][1] == $l - 1 then .[-1][1] = $l else . + [[$l, $l]] end);

    # Map "<module path>/pkg/file.go" to "<module dir>/pkg/file.go" using the longest module prefix.
    def repo_path:
      . as $f
      | [$mods[0][] | . as $m | select($f | startswith($m.path + "/"))]
      | sort_by(.path | length)
      | last
      | if . == null then null
        else (if .dir == "." then "" else .dir + "/" end) + $f[(.path | length) + 1:]
        end;

    [inputs
      | select(length > 0 and (startswith("mode:") | not))
      | capture("^(?<f>.+):(?<sl>[0-9]+)\\.[0-9]+,(?<el>[0-9]+)\\.[0-9]+ (?<n>[0-9]+) (?<h>[0-9]+)$")
      | {f: (.f | repo_path), sl: (.sl | tonumber), el: (.el | tonumber), n: (.n | tonumber), h: (.h | tonumber)}
      | select(.f != null and .n > 0)]
    | group_by(.f)
    | map({key: .[0].f, value: .})
    | from_entries as $blocks
    | {
        base: $base,
        profile: $profile,
        files: [
          $changes[0] | group_by(.file)[] | . as $ranges
          | $ranges[0].file as $file
          | ($blocks[$file] // []) as $file_blocks
          | [$ranges[] | range(.start; .end + 1)] | unique as $lines
          | reduce $lines[] as $l ({hit: [], missed: [], partial: []};
              [$file_blocks[] | select(.sl <= $l and $l <= .el)] as $over
              | if ($over | length) == 0 then .
                elif ($over | all(.h > 0)) then .hit += [$l]
                elif ($over | any(.h > 0)) then .partial += [$l]
                else .missed += [$l]
                end)
          | (.hit | length) as $hit
          | (.hit + .missed + .partial | length) as $tracked
          | {
              file: $file,
              tracked: $tracked,
              hit: $hit,
              missed: (.missed | ranges),
              partial: (.partial | ranges),
              percent: (if $tracked == 0 then null else (($hit * 10000 / $tracked | round) / 100) end)
            }
        ]
      }'
}

# Print the summary of the JSON report; one line per file with tracked lines.
print_summary() {
  local report=$1
  jq -r '
    def fmt: map(if .[0] == .[1] then "\(.[0])" else "\(.[0])-\(.[1])" end) | join(",");
    .files[] | select(.tracked > 0)
    | "\(.file): \(.hit)/\(.tracked) lines hit (\(.percent)%)"
      + (if (.missed | length) > 0 then "\n  missed:  \(.missed | fmt)" else "" end)
      + (if (.partial | length) > 0 then "\n  partial: \(.partial | fmt)" else "" end)
  ' "$report"
}

main() {
  local base=${1:-origin/main}
  local profile=${2:-cover.out}
  local root merge_base tmpdir has_gaps

  command -v jq >/dev/null || die "jq is required"
  root=$(git rev-parse --show-toplevel 2>/dev/null) || die "not inside a git repository"
  [[ $profile == /* ]] || profile=$PWD/$profile
  [[ -f $profile ]] || die "coverage profile $profile not found, run a make test target first"
  cd "$root"
  git rev-parse --verify -q "$base^{commit}" >/dev/null || die "unknown base ref $base"
  merge_base=$(git merge-base "$base" HEAD) || die "no merge base between $base and HEAD"

  tmpdir=$(mktemp -d)
  # tmpdir is local to main, so expand it now instead of when the trap fires.
  # shellcheck disable=SC2064
  trap "rm -rf '$tmpdir'" EXIT

  log "checking $profile is newer than the changed .go files"
  check_profile_fresh "$merge_base" "$profile"

  log "collecting changed lines against $base"
  changed_line_ranges "$merge_base" | jq -Rn '[inputs | split("\t") | {file: .[0], start: (.[1] | tonumber), end: (.[2] | tonumber)}]' >"$tmpdir/changes.json"
  module_map | jq -Rn '[inputs | split("\t") | {path: .[0], dir: .[1]}]' >"$tmpdir/modules.json"

  log "joining changed lines with $profile"
  mkdir -p "$OUT_DIR"
  # shellcheck disable=SC2094 # $profile is only read; the report goes to $OUT_FILE
  build_report "$base" "$profile" "$tmpdir/modules.json" "$tmpdir/changes.json" <"$profile" | jq -c . >"$OUT_FILE"
  log "wrote $OUT_FILE"

  print_summary "$OUT_FILE"
  has_gaps=$(jq -r 'any(.files[]; (.missed | length) > 0 or (.partial | length) > 0)' "$OUT_FILE")
  if [[ $has_gaps == true ]]; then
    exit 1
  fi
  echo "all changed lines with statements are covered"
}

if [[ ${BASH_SOURCE[0]} == "$0" ]]; then
  main "$@"
fi
