#!/usr/bin/env bash
# Samples cgroup state of byohost-* containers. Read-only against Docker.
set -Eeuo pipefail
shopt -s nullglob

usage() {
  cat <<'EOF'
Usage: collect-cgroup-state.sh --out-dir DIR [--interval SECONDS] [--include-existing] [--duration SECONDS]

  --out-dir DIR         Output directory (required). Nothing is written outside it.
  --interval SECONDS    Seconds between samples (default 5).
  --include-existing    Also watch byohost-* containers created before this script started.
  --duration SECONDS    Stop after this long (default 0: run until SIGTERM or SIGINT).

Writes cgroup-samples.jsonl, failure-<container>/, host-info.txt and docker-events.log to DIR.
Only read-only docker commands are used.
EOF
}

OUT_DIR=""
INTERVAL=5
INCLUDE_EXISTING=0
DURATION=0
EXEC_TIMEOUT=10
LOG_FILE=""
EVENTS_PID=""
SLEEP_PID=""

# Container name -> "yes" or "no": whether it is watched.
declare -A WATCHED=()
# Container name -> 1 once its failure snapshot exists.
declare -A SNAPSHOTTED=()

log() {
  local line
  line="$(date -u +%FT%TZ) $*"
  echo "$line"
  if [[ -n "$LOG_FILE" ]]; then
    echo "$line" >>"$LOG_FILE"
  fi
}

cleanup() {
  if [[ -n "$SLEEP_PID" ]]; then
    kill "$SLEEP_PID" 2>/dev/null || true
  fi
  if [[ -n "$EVENTS_PID" ]]; then
    kill "$EVENTS_PID" 2>/dev/null || true
    wait "$EVENTS_PID" 2>/dev/null || true
  fi
}

parse_args() {
  while [[ $# -gt 0 ]]; do
    case "$1" in
    --out-dir)
      OUT_DIR=${2:?--out-dir needs a value}
      shift 2
      ;;
    --interval)
      INTERVAL=${2:?--interval needs a value}
      shift 2
      ;;
    --duration)
      DURATION=${2:?--duration needs a value}
      shift 2
      ;;
    --include-existing)
      INCLUDE_EXISTING=1
      shift
      ;;
    -h | --help)
      usage
      exit 0
      ;;
    *)
      echo "unknown argument: $1" >&2
      usage >&2
      exit 2
      ;;
    esac
  done
  if [[ -z "$OUT_DIR" ]]; then
    echo "--out-dir is required" >&2
    usage >&2
    exit 2
  fi
  [[ "$INTERVAL" =~ ^[0-9]+$ && "$INTERVAL" -gt 0 ]] || {
    echo "--interval must be a positive integer" >&2
    exit 2
  }
  [[ "$DURATION" =~ ^[0-9]+$ ]] || {
    echo "--duration must be a non-negative integer" >&2
    exit 2
  }
}

# cexec runs a command in a container, bounded by EXEC_TIMEOUT.
cexec() {
  local container=$1
  shift
  timeout "$EXEC_TIMEOUT" docker exec "$container" "$@" 2>/dev/null
}

record_host_info() {
  local f="$OUT_DIR/host-info.txt"
  {
    echo "== date"
    date -u +%FT%TZ
    echo "== docker version"
    docker version 2>&1 || true
    echo "== docker info"
    docker info 2>&1 || true
    echo "== uname -r"
    uname -r
    echo "== systemd --version"
    systemd --version 2>&1 || true
    echo "== df -h /"
    df -h / 2>&1 || true
  } >"$f"
  log "recorded host info to $f"
}

start_events() {
  docker events >>"$OUT_DIR/docker-events.log" 2>&1 &
  EVENTS_PID=$!
  log "started docker events (pid $EVENTS_PID)"
}

# is_watched decides once per container name whether it is in scope.
is_watched() {
  local container=$1 created created_ns
  if [[ -n "${WATCHED[$container]:-}" ]]; then
    [[ "${WATCHED[$container]}" == "yes" ]]
    return
  fi
  if [[ "$INCLUDE_EXISTING" -eq 1 ]]; then
    WATCHED[$container]=yes
    return 0
  fi
  created=$(docker inspect -f '{{.Created}}' "$container" 2>/dev/null || true)
  created_ns=$(date -d "$created" +%s%N 2>/dev/null || echo 0)
  if [[ "$created_ns" -ge "$START_NS" ]]; then
    WATCHED[$container]=yes
    log "watching new container $container"
    return 0
  fi
  WATCHED[$container]=no
  return 1
}

sample_container() {
  local c=$1
  local root_ctl root_sub kp_exists=false kp_ctl="" kp_sub="" state kubelet
  root_ctl=$(cexec "$c" cat /sys/fs/cgroup/cgroup.controllers || true)
  root_sub=$(cexec "$c" cat /sys/fs/cgroup/cgroup.subtree_control || true)
  if cexec "$c" test -d /sys/fs/cgroup/kubepods.slice; then
    kp_exists=true
    kp_ctl=$(cexec "$c" cat /sys/fs/cgroup/kubepods.slice/cgroup.controllers || true)
    kp_sub=$(cexec "$c" cat /sys/fs/cgroup/kubepods.slice/cgroup.subtree_control || true)
  fi
  state=$(cexec "$c" systemctl is-system-running || true)
  kubelet=$(cexec "$c" systemctl is-active kubelet || true)
  jq -cn \
    --arg ts "$(date -u +%FT%TZ)" \
    --arg container "$c" \
    --arg root_ctl "$root_ctl" \
    --arg root_sub "$root_sub" \
    --argjson kp_exists "$kp_exists" \
    --arg kp_ctl "$kp_ctl" \
    --arg kp_sub "$kp_sub" \
    --arg state "$state" \
    --arg kubelet "$kubelet" \
    '{timestamp: $ts, container: $container,
      root: {controllers: $root_ctl, subtree_control: $root_sub},
      kubepods_slice: {exists: $kp_exists, controllers: $kp_ctl, subtree_control: $kp_sub},
      system_running: $state,
      kubelet_active: ($kubelet == "active"),
      kubelet_state: $kubelet}' >>"$OUT_DIR/cgroup-samples.jsonl"
}

snapshot_failure() {
  local c=$1 dir="$OUT_DIR/failure-$1"
  mkdir -p "$dir"
  log "FAILURE: kubelet journal in $c contains 'missing controllers', snapshot in $dir"
  docker inspect "$c" >"$dir/inspect.json" 2>&1 || true
  jq '.[0] | {Name, Created, State,
      CgroupnsMode: .HostConfig.CgroupnsMode,
      Privileged: .HostConfig.Privileged,
      CgroupParent: .HostConfig.CgroupParent}' "$dir/inspect.json" >"$dir/inspect-summary.json" 2>&1 || true
  cexec "$c" cat /proc/1/cgroup >"$dir/proc-1-cgroup.txt" || true
  cexec "$c" cat /sys/fs/cgroup/cgroup.controllers >"$dir/root-cgroup.controllers" || true
  cexec "$c" cat /sys/fs/cgroup/cgroup.subtree_control >"$dir/root-cgroup.subtree_control" || true
  cexec "$c" wc -l /sys/fs/cgroup/cgroup.procs >"$dir/root-cgroup.procs-linecount.txt" || true
  cexec "$c" systemctl show kubepods.slice -p Delegate -p ControlGroup >"$dir/kubepods-slice-show.txt" || true
  timeout 20 docker exec "$c" journalctl -u kubelet --no-pager -n 200 >"$dir/kubelet-journal.txt" 2>&1 || true
  cexec "$c" systemd --version >"$dir/systemd-version.txt" || true
  docker info >"$dir/docker-info.txt" 2>&1 || true
}

check_container() {
  local c=$1
  if [[ -n "${SNAPSHOTTED[$c]:-}" ]]; then
    return 0
  fi
  if timeout 20 docker exec "$c" journalctl -u kubelet --no-pager -n 50 2>/dev/null | grep -q "missing controllers"; then
    SNAPSHOTTED[$c]=1
    snapshot_failure "$c"
  fi
}

main() {
  parse_args "$@"
  mkdir -p "$OUT_DIR"
  LOG_FILE="$OUT_DIR/collector.log"
  START_NS=$(date +%s%N)
  local start_s=$SECONDS
  trap cleanup EXIT
  trap 'exit 0' TERM INT

  log "collector starting: out-dir=$OUT_DIR interval=$INTERVAL include-existing=$INCLUDE_EXISTING duration=$DURATION"
  record_host_info
  start_events

  local names c
  while true; do
    mapfile -t names < <(docker ps --filter name=byohost- --format '{{.Names}}' 2>/dev/null || true)
    for c in "${names[@]}"; do
      [[ "$c" == byohost-* ]] || continue
      is_watched "$c" || continue
      sample_container "$c"
      check_container "$c"
    done
    if [[ "$DURATION" -gt 0 && $((SECONDS - start_s)) -ge "$DURATION" ]]; then
      log "duration reached, stopping"
      break
    fi
    sleep "$INTERVAL" &
    SLEEP_PID=$!
    wait "$SLEEP_PID" || true
    SLEEP_PID=""
  done
}

main "$@"
