# build the web-pixi client (vite → web-pixi/dist) — required before go
# build so the webpixi package's //go:embed has real content to include.
build-web:
    cd web-pixi && bun install --frozen-lockfile && bun run build

# build the Go binary only (assumes web-pixi/dist already exists)
build-go:
    go build -o bin/server ./cmd/server

# build TS SDK + web client + server into bin/server
# space-sdk runs first so the web client picks up any schema changes.
build: space-sdk build-web admin-build build-go

# build + run
run: build
    ./bin/server

# regenerate protobuf (buf generate)
proto:
    buf generate

# build the bot client
botclient:
    go build -o bin/botclient ./cmd/botclient

# generate typed TS client SDK (e.g. just client-sdk examples/4node-basic)
# Passes POSTGRES_URL through so service-kind-bearing example games (auth, echo,
# anything with RequiresDB) can build their schema without panicking.
client-sdk GAME:
    go run ./{{ GAME }} --dump-schema "--postgres-url={{ env('POSTGRES_URL', 'postgres://mmo:mmo@localhost:5432/mmo?sslmode=disable') }}" | go run ./cmd/sdkgen \
        --out {{ GAME }}/web/sdk \
        --core pkg/quantize/ts/delta-decoder-core.ts

# generate typed TS client SDK for the space game → web-pixi/sdk/
space-sdk:
    go run ./cmd/server --dump-schema | go run ./cmd/sdkgen \
        --out web-pixi/sdk \
        --core pkg/quantize/ts/delta-decoder-core.ts

# remove bin/
clean:
    rm -rf bin/

# build + run server & web-pixi vite dev server
dev: build
    #!/usr/bin/env bash
    set -euo pipefail
    tmux kill-session -t space-vite 2>/dev/null || true
    tmux new-session -d -s space-vite -c "{{ justfile_directory() }}/web-pixi" 'bun run dev'
    trap 'tmux kill-session -t space-vite 2>/dev/null' INT TERM EXIT
    ./bin/server --dev-insecure-cookie

# Tmux session `space-dist` with coord + 3 nodes + gateway panes, each
# with its own interactive console. Logs mirror to log/distributed-space
# via tmux pipe-pane (keeps the pane's TTY intact for readline). Browser
# at http://localhost:8080 — gateway serves the embedded web-pixi/dist.

# run the space game in 5-process distributed mode (tmux)
distributed-space: build db-up
    #!/usr/bin/env bash
    set -euo pipefail
    command -v tmux >/dev/null 2>&1 || { echo "tmux required: sudo apt install tmux  OR  brew install tmux"; exit 1; }
    root="{{ justfile_directory() }}"
    bin="$root/bin/server"
    coord_addr="localhost:9100"
    logdir="$root/log/distributed-space"
    tmux kill-session -t space-dist 2>/dev/null || true
    mkdir -p "$logdir"

    # Layout: coordinator as a wide bottom pane (30% of window height),
    # workers + gateway as a thin equal-width row above it.
    #
    #   +------+------+------+------+
    #   | h-0  | h-1  | h-2  |  gw  |   70%  (worker row)
    #   +------+------+------+------+
    #   |       coordinator         |   30%  (admin + fan-out target)
    #   +---------------------------+
    #
    # Create coordinator first (it becomes the bottom pane via -v -b on
    # the first split). Each subsequent -h split targets the preceding
    # pane by index and uses -l PCT to size the NEW pane as a fraction
    # of the SPLIT target — yielding four ~equal columns up top without
    # needing a window-wide layout that would also resize the bottom.
    tmux new-session -d -s space-dist -c "$root" \
        "$bin --mode=coordinator --control-listen=:9100"
    tmux pipe-pane -t space-dist -o "cat > $logdir/coordinator.log"

    tmux split-window -t space-dist -v -b -l 70% -c "$root" \
        "$bin --mode=host --coordinator-addr=$coord_addr --host-id=space-host-0"
    tmux pipe-pane -t space-dist -o "cat > $logdir/host-0.log"

    # Top pane now holds host-0; split it into four equal columns.
    tmux split-window -t space-dist -h -l 75% -c "$root" \
        "$bin --mode=host --coordinator-addr=$coord_addr --host-id=space-host-1"
    tmux pipe-pane -t space-dist -o "cat > $logdir/host-1.log"

    tmux split-window -t space-dist -h -l 66% -c "$root" \
        "$bin --mode=host --coordinator-addr=$coord_addr --host-id=space-host-2"
    tmux pipe-pane -t space-dist -o "cat > $logdir/host-2.log"

    # Gateway also runs the auth service (--mode=gateway,service) so the
    # /auth/* HTTPS endpoints mount on the gateway's HTTP listener — same
    # process the browser hits at :8080. The auth kind needs Postgres,
    # hence --postgres-url. --dev-insecure-cookie keeps cookies usable
    # over plain HTTP localhost. The URL is single-quoted so zsh (tmux's
    # default-shell on this box) doesn't glob-expand the `?` in
    # `?sslmode=disable` and refuse to launch the binary.
    tmux split-window -t space-dist -h -l 50% -c "$root" \
        "$bin --mode=gateway,service --services=auth --coordinator-addr=$coord_addr --gateway-id=space-gw-0 --port=8080 '--postgres-url=postgres://mmo:mmo@localhost:5432/mmo?sslmode=disable' --dev-insecure-cookie"
    tmux pipe-pane -t space-dist -o "cat > $logdir/gateway.log"

    # Focus on the coordinator pane so the user lands at the admin prompt.
    # Use {bottom-left} (positional) rather than pane index — tmux renumbers
    # by position after splits, so the first-created pane isn't index 0.
    tmux select-pane -t 'space-dist.{bottom-left}'

    tmux attach-session -t space-dist

# kill the distributed-space tmux session (all 5 processes)
distributed-space-stop:
    tmux kill-session -t space-dist 2>/dev/null || true

# tail all distributed-space pane logs
distributed-space-logs:
    tail -F {{ justfile_directory() }}/log/distributed-space/*.log

# run the unit-level coverage for the distributed-commands + entity-tp work.
# Exercises every new entity.* / player.* engine command, the routing layer,
# and the offline-aware ResolvePlayerTarget helper. End-to-end verification
# (cross-host TP, distributed list/info) lives in the 4node-basic e2e test
# and the manual `just distributed-space` console.
smoke-commands:
    go test -v ./pkg/universe/ \
        -run "TestEntity|TestPlayer|TestResolve|TestPickDBHost|TestMoveEntityTo|TestHandoffDriver_AcceptsNonNeighbor|TestHandoffDriver_BypassCooldown"
    go test -v ./pkg/cmdsys/ -run "TestRouteKindString_PlayerHomeOrOwner|TestLocalContext_AcceptsLocalProcess"
    go test -v ./pkg/mmokit/ -run "TestMmokitFacade_ExportsMoveEntityAPI"
    go test -v ./internal/game/ -run "TestPlayerDataAccessor_RoundTrip|TestRepoLocator_HitAndMiss"

# delete game databases
resetdb:
    rm -f data/gameserver.db
    rm -f data/marketplace.db

# reset databases and run dev
freshdev: resetdb dev

# start prometheus
prometheus:
    #!/usr/bin/env bash
    set -euo pipefail
    which prometheus >/dev/null 2>&1 || { echo "Install: sudo apt install prometheus  OR  brew install prometheus"; exit 1; }
    prometheus --config.file={{ justfile_directory() }}/prometheus.yml \
        --storage.tsdb.path={{ justfile_directory() }}/data/prometheus \
        --web.listen-address=:9090 &
    echo "Prometheus UI: http://localhost:9090"

# stop prometheus
prometheus-stop:
    pkill -f 'prometheus.*config.file' 2>/dev/null && echo "Prometheus stopped" || echo "Prometheus not running"

# reload prometheus config
prometheus-reload:
    pkill -HUP -f 'prometheus.*config.file' 2>/dev/null && echo "Prometheus config reloaded" || echo "Prometheus not running"

# restart prometheus
prometheus-restart: prometheus-stop
    sleep 1
    just prometheus

# start postgres via docker compose
db-up:
    docker compose up -d postgres

# stop postgres
db-down:
    docker compose down

# drop into psql shell
db-psql:
    docker compose exec postgres psql -U mmo -d mmo

# wipe the postgres volume and restart
db-reset:
    docker compose down -v
    docker compose up -d postgres

# run Postgres integration tests (requires `just db-up` first)
# -p 1 serializes test packages: every pgtest suite shares one Postgres
# database and TRUNCATEs the same tables in setup, so parallel package
# execution would race.
test-pg:
    POSTGRES_URL=postgres://mmo:mmo@localhost:5432/mmo?sslmode=disable \
        go test -p 1 -count=1 -tags=pgtest \
            ./pkg/persist/... ./internal/persist/... ./pkg/services/...

# build the admin SPA into pkg/admin/static/dist (consumed by //go:embed)
admin-build:
    cd web-admin && bun install --frozen-lockfile && bun run build

# diagnostic — sample the server's /probe-ws heartbeat endpoint to
# isolate whether bursty WebSocket delivery is a server / network /
# browser issue. Requires `just dev` (or any server with the /probe-ws
# route) to be running. See diagnostics/probe.ts for what's measured.
#
# Args are POSITIONAL (just doesn't support name=value on recipe args).
#
#   just probe                    # 30s sample at localhost:8080 (defaults)
#   just probe 60                 # 60s sample at localhost:8080
#   just probe 30 localhost:8080  # both args explicit
probe duration="30" host="localhost:8080":
    bun run diagnostics/probe.ts --host={{host}} --duration={{duration}}

# probe with JSON output — for scripting / CI assertions
probe-json duration="30" host="localhost:8080":
    bun run diagnostics/probe.ts --host={{host}} --duration={{duration}} --json

# vite dev server with proxy to a running coordinator's --admin-listen=:9101
admin-dev:
    cd web-admin && bun install && bun run dev

# vitest unit tests for lib/*
admin-test:
    cd web-admin && bun run test

# svelte-check (typecheck + vite-plugin-svelte warnings as errors)
admin-typecheck:
    cd web-admin && bun install --frozen-lockfile && bun run typecheck

# Enforces the no-ark-in-game architectural invariant — fails if any
# non-exempted file in internal/game/ imports github.com/mlange-42/ark/ecs.
lint-no-ark:
    ./scripts/no_ark_in_game.sh
