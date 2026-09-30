#!/usr/bin/env bash
set -Eeuo pipefail

# generate-e2e-matrix.sh - converts test/e2e/pict/generated-matrix.tsv into a
# GitHub Actions `strategy.matrix.include` JSON array.
#
# Each row is enriched here with the concrete invocation details its
# Scenario implies (which suite, which GINKGO_FOCUS, which env vars) so the
# consuming workflow step stays a generic dispatcher instead of duplicating
# scenario-specific logic once per matrix case.
#
# Usage: generate-e2e-matrix.sh <path-to-generated-matrix.tsv>
# Writes "matrix=<json>" to $GITHUB_OUTPUT if set, otherwise prints the JSON
# to stdout (for local debugging via `make generate-e2e-matrix-json`).

# Ginkgo v2's -ginkgo.focus reports "SUCCESS!" and exits 0 when it matches
# zero specs (verified directly: a typo'd focus runs 0 of N specs and still
# passes) -- a Describe() tag renamed in a spec file without updating the
# case statement above would silently turn a matrix case into a no-op that
# reports green forever. Catch that here, once, before the expensive matrix
# job starts, by checking every focus value actually appears in the source
# it's supposed to select.
verify_focus_values_are_real() {
  local json=$1
  local suite focus dir stale=0

  while IFS= read -r obj; do
    suite=$(jq -r '.suite' <<<"${obj}")
    focus=$(jq -r '.ginkgo_focus' <<<"${obj}")
    [[ -z "${focus}" ]] && continue
    dir="test/e2e"
    [[ "${suite}" == "packaging" ]] && dir="test/e2e/packaging"

    # @tsv would double-escape the backslashes in a \[Tag\] focus regex, so
    # fields are pulled directly off each object above instead. Unescape
    # \[ / \] back to literal [ / ] to grep for the plain Describe() text.
    local literal=${focus//\\[/[}
    literal=${literal//\\]/]}

    if ! grep -rFq -- "${literal}" "${dir}"/*.go; then
      echo "generate-e2e-matrix.sh: GINKGO_FOCUS '${focus}' (literal: '${literal}') matches no Describe() under ${dir}/*.go -- stale mapping in this script" >&2
      stale=1
    fi
  done < <(jq -c '.[]' <<<"${json}")

  if ((stale)); then
    echo "generate-e2e-matrix.sh: refusing to generate a matrix with stale focus values -- a case above would silently run 0 specs and report success" >&2
    exit 1
  fi
}

main() {
  local tsv=$1
  local rows="[]"

  while IFS=$'\t' read -r scenario k8s_version; do
    local suite="e2e" ginkgo_focus="" kubernetes_version="" \
      e2e_k8s_version_from="" e2e_k8s_version_to=""

    case "${scenario}" in
      Join)                ginkgo_focus='\[PR-Blocking\]' ;;
      Installer)           ginkgo_focus='\[Installer\]' ;;
      ByoHCtl)              ginkgo_focus='\[Byohctl\]' ;;
      Reuse)                ginkgo_focus='\[Reuse\]' ;;
      ClusterClass)         ginkgo_focus='\[Cluster-Class\]' ;;
      MDScale)              ginkgo_focus='\[MD-Scale\]' ;;
      UpgradeCluster)       ginkgo_focus='\[K8s-Upgrade-Cluster\]' ;;
      UpgradeClusterClass)  ginkgo_focus='\[K8s-Upgrade-ClusterClass\]' ;;
      PackagingDeb)         suite="packaging"; ginkgo_focus="pf9-byohost deb" ;;
      PackagingRpm)         suite="packaging"; ginkgo_focus="pf9-byohost RPM" ;;
      *)
        echo "generate-e2e-matrix.sh: unknown Scenario '${scenario}' in ${tsv}" >&2
        exit 1
        ;;
    esac

    # e2e_suite_test.go's SynchronizedBeforeSuite builds a local k8s bundle
    # for KUBERNETES_VERSION unconditionally, before any spec runs,
    # regardless of GINKGO_FOCUS -- confirmed by running this: leaving it
    # unset for ByoHCtl/UpgradeCluster/UpgradeClusterClass failed the whole
    # suite's setup with 'unexpected Kubernetes version format ""', even
    # though none of those three scenarios read KUBERNETES_VERSION in their
    # own spec body. clusterctl's GetVariableOrEmpty (unlike this repo's own
    # getEnvOrDefault, used for E2E_K8S_VERSION_FROM/_TO) treats an
    # explicitly-empty env var as set, so it can't be left blank the way
    # those two safely can. Every suite=="e2e" row needs a real value here;
    # only suite=="packaging" rows (a separate Go test binary, no
    # SynchronizedBeforeSuite) are exempt.
    if [[ "${suite}" == "e2e" ]]; then
      if [[ "${k8s_version}" == "NA" ]]; then
        kubernetes_version="v1.31.0"
      else
        kubernetes_version="${k8s_version}"
      fi
    fi

    case "${scenario}" in
      UpgradeCluster | UpgradeClusterClass)
        e2e_k8s_version_from="${k8s_version}"
        # model.pict has no upgrade-target column -- it has no independent
        # freedom to cross (see the model's own comment) -- so this is the
        # one place the target is decided, matching this repo's current
        # real default (cluster_upgrade_test.go/clusterclass_upgrade_test.go's
        # E2E_K8S_VERSION_TO default).
        e2e_k8s_version_to="v1.31.2"
        ;;
    esac

    # GitHub Actions auto-names a matrix job by concatenating every field in
    # its object -- without this, the job title leaks the raw GINKGO_FOCUS
    # regex (e.g. Join's is literally the pre-existing, unrelated
    # "[PR-Blocking]" spec tag from e2e_test.go), which reads as if this
    # gated, optional matrix were blocking something. label is what the
    # workflow's job `name:` displays instead.
    job_label="${scenario}"
    [[ "${suite}" == "e2e" ]] && job_label="${scenario} (${kubernetes_version})"

    # jq's `label $out | ...`/`break $out` control-flow keyword makes
    # $label itself unparseable as a --arg/variable name (confirmed: even
    # `jq -n --arg label 1 '$label'` alone fails on jq 1.6, the version
    # this repo's CI runners have -- unrelated to whether the resulting
    # object *key* is named "label", which works fine either way). Named
    # job_label here to avoid that, independent of the "label" JSON field
    # name below.
    row=$(jq -nc \
      --arg scenario "${scenario}" \
      --arg suite "${suite}" \
      --arg ginkgo_focus "${ginkgo_focus}" \
      --arg kubernetes_version "${kubernetes_version}" \
      --arg e2e_k8s_version_from "${e2e_k8s_version_from}" \
      --arg e2e_k8s_version_to "${e2e_k8s_version_to}" \
      --arg job_label "${job_label}" \
      '{scenario: $scenario, suite: $suite, ginkgo_focus: $ginkgo_focus,
        kubernetes_version: $kubernetes_version,
        e2e_k8s_version_from: $e2e_k8s_version_from,
        e2e_k8s_version_to: $e2e_k8s_version_to,
        label: $job_label}')

    rows=$(jq -c --argjson row "${row}" '. + [$row]' <<<"${rows}")
  done < <(tail -n +2 "${tsv}")

  local json
  json=$(jq -c '.' <<<"${rows}")
  echo "generated $(jq 'length' <<<"${json}") matrix cases from ${tsv}" >&2

  verify_focus_values_are_real "${json}"

  if [[ -n "${GITHUB_OUTPUT:-}" ]]; then
    echo "matrix=${json}" >>"${GITHUB_OUTPUT}"
  else
    echo "${json}"
  fi
}

main "$@"
