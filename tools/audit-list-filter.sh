#!/usr/bin/env bash
# Copyright (c) PRO-Robotech
# SPDX-License-Identifier: BUSL-1.1

# audit-list-filter.sh — CI gate (KAC-219 / RBAC v2 W6) for kacho-compute.
#
# Refuses to ship a `List<Resource>` handler in `internal/handler/` that
# returns rows without consulting `authzfilter.Filter` (the canonical
# RBAC v2 list-filter port wrapped around kacho-iam ListObjects).
#
# Heuristic:
#   1. Collect every `func (h *<Name>Handler) List(...)` under
#      internal/handler/*.go.
#   2. For each candidate file, also grep its body for
#      `ListAllowedIDs` OR `authzfilter.Filter` OR `list_filter.go`-style
#      `applyListFilter`.
#   3. If neither token is found in the handler file, print the
#      candidate path and exit 1.
#
# Whitelisted (admin-only / catalog-style — every authenticated caller
# sees every row by design):
#   - Region    — global geography catalog; reference data.
#   - Zone      — global geography catalog; reference data.
#   - DiskType  — global catalog; reference data.
#
# Override:
#   tools/audit-list-filter.sh --allow="<handler-name>" extends the
#   whitelist.

set -euo pipefail

WHITELIST=("RegionHandler" "ZoneHandler" "DiskTypeHandler")
while [[ ${1:-} == --allow=* ]]; do
  WHITELIST+=("${1#--allow=}")
  shift || true
done

is_whitelisted() {
  local h=$1
  for w in "${WHITELIST[@]}"; do [[ "$w" == "$h" ]] && return 0; done
  return 1
}

ROOT=internal/handler
if [[ ! -d "$ROOT" ]]; then
  echo "audit-list-filter: not in kacho-compute (no $ROOT)" >&2
  exit 0
fi

FAIL=0
while IFS= read -r line; do
  file=${line%%:*}
  handler=$(printf '%s\n' "$line" | sed -nE 's/.*func \(h \*([A-Za-z]+Handler)\) List\(.*/\1/p')
  [[ -z "$handler" ]] && continue
  is_whitelisted "$handler" && continue
  if grep -qE 'ListAllowedIDs|authzfilter\.Filter|applyListFilter' "$file"; then
    continue
  fi
  echo "audit-list-filter: $handler — List handler missing authzfilter wiring"
  echo "  file: $file"
  FAIL=1
done < <(grep -nE '^func \(h \*[A-Za-z]+Handler\) List\(' "$ROOT"/*_handler.go)

if [[ $FAIL -ne 0 ]]; then
  echo
  echo "RBAC v2 (KAC-214) requires every public List<Resource> RPC to filter"
  echo "results through authzfilter.Filter (kacho-iam ListObjects backend)."
  echo "Whitelist the handler (admin-only / catalog) with --allow=<Name>"
  echo "if the bypass is intentional."
  exit 1
fi

echo "audit-list-filter: OK"
