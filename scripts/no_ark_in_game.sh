#!/usr/bin/env bash
set -euo pipefail

# Enforces the architectural invariant that game-side code does not
# import ark/ecs directly. Game code uses mmokit's wrappers instead.
#
# Exempt: framework-binding glue files that legitimately need raw ark
# types (Map1, World) to bridge mmokit's typed-component API to ark's
# storage. Anything new should NOT be added to this allowlist.
#
# Exits non-zero if any non-exempt internal/game/*.go file imports
# github.com/mlange-42/ark/ecs.

ALLOWLIST=(
    "internal/game/var_tail_bindings.go"
    "internal/game/entity_kinds.go"
)

# Build a regex that matches the allowlisted paths.
ALLOW_RE=$(printf '|%s' "${ALLOWLIST[@]}")
ALLOW_RE="(${ALLOW_RE:1})"

OFFENDERS=$(grep -rln "mlange-42/ark/ecs" internal/game/ --include="*.go" 2>/dev/null \
    | grep -v _test.go \
    | grep -vE "$ALLOW_RE" \
    || true)

if [[ -n "${OFFENDERS}" ]]; then
    echo "ERROR: internal/game/ files import ark/ecs directly:"
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

echo "OK: no ark/ecs imports in internal/game/ (except allowlisted glue)"
