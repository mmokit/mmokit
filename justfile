# build the web-pixi client (vite → web-pixi/dist) — required before go
# build so the webpixi package's //go:embed has real content to include.
build-web:
    cd web-pixi && bun install --frozen-lockfile && bun run build

# build the Go binary only (assumes web-pixi/dist already exists)
build-go:
    go build -o bin/server ./cmd/server

# build web client + server into bin/server
build: build-web build-go

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
client-sdk GAME:
    go run ./{{ GAME }} --dump-schema | go run ./cmd/sdkgen \
        --out {{ GAME }}/web/sdk \
        --proto-es gen/es \
        --core pkg/quantize/ts/delta-decoder-core.ts

# generate typed TS client SDK for the space game → web-pixi/sdk/
space-sdk:
    go run ./cmd/server --dump-schema | go run ./cmd/sdkgen \
        --out web-pixi/sdk \
        --proto-es gen/es \
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
    ./bin/server

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

    # Each split-window (without -d) makes the new pane active, so the
    # following pipe-pane targets it without needing explicit indices
    # (works regardless of pane-base-index). select-layout tiled after
    # each split redistributes space so the next split has room — else
    # tmux rejects "no space for new pane" on small terminals.
    tmux new-session -d -s space-dist -c "$root" \
        "$bin --mode=coordinator --control-listen=:9100"
    tmux pipe-pane -t space-dist -o "cat > $logdir/coordinator.log"

    tmux split-window -t space-dist -c "$root" \
        "$bin --mode=node --coordinator-addr=$coord_addr --host-id=space-node-0"
    tmux pipe-pane -t space-dist -o "cat > $logdir/node-0.log"
    tmux select-layout -t space-dist tiled

    tmux split-window -t space-dist -c "$root" \
        "$bin --mode=node --coordinator-addr=$coord_addr --host-id=space-node-1"
    tmux pipe-pane -t space-dist -o "cat > $logdir/node-1.log"
    tmux select-layout -t space-dist tiled

    tmux split-window -t space-dist -c "$root" \
        "$bin --mode=node --coordinator-addr=$coord_addr --host-id=space-node-2"
    tmux pipe-pane -t space-dist -o "cat > $logdir/node-2.log"
    tmux select-layout -t space-dist tiled

    tmux split-window -t space-dist -c "$root" \
        "$bin --mode=gateway --coordinator-addr=$coord_addr --gateway-id=space-gw-0 --port=8080"
    tmux pipe-pane -t space-dist -o "cat > $logdir/gateway.log"
    tmux select-layout -t space-dist tiled

    tmux attach-session -t space-dist

# kill the distributed-space tmux session (all 5 processes)
distributed-space-stop:
    tmux kill-session -t space-dist 2>/dev/null || true

# tail all distributed-space pane logs
distributed-space-logs:
    tail -F {{ justfile_directory() }}/log/distributed-space/*.log

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
test-pg:
    POSTGRES_URL=postgres://mmo:mmo@localhost:5432/mmo?sslmode=disable \
        go test -count=1 -tags=pgtest ./pkg/persist/...
