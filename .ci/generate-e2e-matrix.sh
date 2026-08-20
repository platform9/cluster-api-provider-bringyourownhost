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

    case "${scenario}" in
      Join | Installer | Reuse | ClusterClass | MDScale)
        kubernetes_version="${k8s_version}"
        ;;
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

    row=$(jq -nc \
      --arg scenario "${scenario}" \
      --arg suite "${suite}" \
      --arg ginkgo_focus "${ginkgo_focus}" \
      --arg kubernetes_version "${kubernetes_version}" \
      --arg e2e_k8s_version_from "${e2e_k8s_version_from}" \
      --arg e2e_k8s_version_to "${e2e_k8s_version_to}" \
      '{scenario: $scenario, suite: $suite, ginkgo_focus: $ginkgo_focus,
        kubernetes_version: $kubernetes_version,
        e2e_k8s_version_from: $e2e_k8s_version_from,
        e2e_k8s_version_to: $e2e_k8s_version_to}')

    rows=$(jq -c --argjson row "${row}" '. + [$row]' <<<"${rows}")
  done < <(tail -n +2 "${tsv}")

  local json
  json=$(jq -c '.' <<<"${rows}")
  echo "generated $(jq 'length' <<<"${json}") matrix cases from ${tsv}" >&2

  if [[ -n "${GITHUB_OUTPUT:-}" ]]; then
    echo "matrix=${json}" >>"${GITHUB_OUTPUT}"
  else
    echo "${json}"
  fi
}

main "$@"
