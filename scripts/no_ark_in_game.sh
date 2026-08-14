#!/usr/bin/env bash
set -euo pipefail

# Enforces the architectural invariant that game-side code does not
# import ark/ecs directly. Game code uses mmokit's wrappers instead.
#
# Usage: no_ark_in_game.sh [GAME_DIR]
#   GAME_DIR defaults to internal/game. Pass the game layer's directory
#   when the reference game lives somewhere else (an example, say).
#
# Exempt: framework-binding glue files that legitimately need raw ark
# types (Map1, World) to bridge mmokit's typed-component API to ark's
# storage. Anything new should NOT be added to this allowlist.
#
# Exits 1 if any non-exempt <GAME_DIR>/*.go file imports
# github.com/mlange-42/ark/ecs, and 2 if GAME_DIR does not exist.
#
# The missing-directory check is load-bearing, not defensive. `grep -r`
# on an absent path writes to stderr and exits 2, and the pipeline below
# ends in `|| true` so that both the message and the status are
# swallowed: without this guard the script printed "OK" and exited 0
# whenever the directory it is supposed to police had been renamed away.
# A gate that passes when its subject does not exist is worse than none.

DIR="${1:-internal/game}"
DIR="${DIR%/}"

if [[ ! -d "${DIR}" ]]; then
    echo "ERROR: game directory not found: ${DIR}" >&2
    echo "Pass the game layer's directory as the first argument." >&2
    exit 2
fi

# Allowlisted paths are relative to DIR so the list survives a move.
ALLOWLIST=(
    "var_tail_bindings.go"
    "entity_kinds.go"
)

# Build a regex matching the allowlisted paths, anchored under DIR.
ALLOW_RE=""
for f in "${ALLOWLIST[@]}"; do
    ALLOW_RE="${ALLOW_RE}|^${DIR}/${f}$"
done
ALLOW_RE="(${ALLOW_RE:1})"

OFFENDERS=$(grep -rln "mlange-42/ark/ecs" "${DIR}/" --include="*.go" \
    | grep -v _test.go \
    | grep -vE "$ALLOW_RE" \
    || true)

if [[ -n "${OFFENDERS}" ]]; then
    echo "ERROR: ${DIR}/ files import ark/ecs directly:"
    echo "${OFFENDERS}"
    echo ""
    echo "Use mmokit wrappers instead:"
    echo "  - mmokit.Entity / mmokit.EntityHandle for entity types"
    echo "  - mmokit.AddComponent / RemoveComponent for structural mutations"
    echo "  - mmokit.Any / FindOne / ForEach1/2/3 for queries"
    echo "  - s.Commands().Despawn / Defer for entity/game-action deferral"
    echo ""
    echo "If your file is genuinely framework-binding glue that needs raw ark,"
    echo "add it to the ALLOWLIST in scripts/no_ark_in_game.sh (rare)."
    exit 1
fi

echo "OK: no ark/ecs imports in ${DIR}/ (except allowlisted glue)"
