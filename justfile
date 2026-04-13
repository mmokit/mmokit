# compile server to bin/server
build:
    go build -o bin/server ./cmd/server

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
    ./bin/server --dynamic-cells

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
