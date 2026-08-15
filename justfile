# build all hot-swappable wasm system modules into dist/wasmmods/
wasm-build:
    mkdir -p dist/wasmmods
    GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o dist/wasmmods/tint.wasm ./examples/4node-basic/wasmmods/tint/

# Framework-wide build. This used to be `space-sdk build-web admin-build
# build-go` — three-quarters of it belonged to the reference game, and
# space-sdk needed a live PostgreSQL because the game opens its database
# before --dump-schema exits. Each example builds itself now:
#
#   cd examples/simple      && just run
#   cd examples/4node-basic && just run
#   cd examples/space       && just run
build: admin-build

# regenerate protobuf (buf generate)
proto:
    buf generate

# generate typed TS client SDK (e.g. just client-sdk examples/4node-basic)
# Passes POSTGRES_URL through so service-kind-bearing example games (auth, echo,
# anything with RequiresDB) can build their schema without panicking.
client-sdk GAME:
    go run ./{{ GAME }} --dump-schema --control-listen= --admin-listen= "--postgres-url={{ env('POSTGRES_URL', 'postgres://mmo:mmo@localhost:5432/mmo?sslmode=disable') }}" | go run ./cmd/sdkgen \
        --out {{ GAME }}/web/sdk \
        --core pkg/quantize/ts/delta-decoder-core.ts

# remove bin/
clean:
    rm -rf bin/

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
# Engine and service persistence only. A game's own persistence tests live in
# its example (`cd examples/space && just test-pg`), against that example's own
# database — this recipe TRUNCATEs shared tables in the bare `mmo` database.
test-pg:
    POSTGRES_URL=postgres://mmo:mmo@localhost:5432/mmo?sslmode=disable \
        go test -p 1 -count=1 -tags=pgtest \
            ./pkg/persist/... ./pkg/services/...

# build the admin SPA into pkg/admin/static/dist (consumed by //go:embed)
admin-build:
    cd web-admin && bun install --frozen-lockfile && bun run build

# vite dev server with proxy to a running coordinator's --admin-listen=:9101
admin-dev:
    cd web-admin && bun install && bun run dev

# vitest unit tests for lib/*
admin-test:
    cd web-admin && bun run test

# svelte-check (typecheck + vite-plugin-svelte warnings as errors)
admin-typecheck:
    cd web-admin && bun install --frozen-lockfile && bun run typecheck

# Enforces the no-ark-in-game architectural invariant for every example that
# has a game layer — fails if a non-exempted file imports
# github.com/mlange-42/ark/ecs directly. The script exits 2 if the directory
# it is given does not exist, so a renamed game layer fails loudly here rather
# than passing while checking nothing.
lint-no-ark:
    ./scripts/no_ark_in_game.sh examples/space/internal/game

# mutate-fuzz every decoder family docs/roadmap.md §6.7 names (CE-002
# criterion 6). A smoke run, not a campaign; the real mutation budget belongs
# to a scheduled CI job.
#
# Seeds alone run under a plain `go test`; this recipe is the only thing that
# MUTATES them. Two consequences worth knowing before you run it:
#
#   - Go writes every crasher it finds into that target's
#     testdata/fuzz/<Target>/ directory, and a committed crasher reddens
#     `go test ./...` for everyone (§6.8.4). Move a discovered crasher out of
#     testdata before committing — unless you are landing the checked decoder
#     in the same commit, which is exactly where those seeds belong.
#   - Until the checked decoder lands, FuzzReflectUnmarshal is EXPECTED to
#     crash within seconds. That is the harness working, not a broken recipe.
#     It runs last so the other five still get their budget.
#
# Args are POSITIONAL.
#
#   just fuzz      # 20s per target
#   just fuzz 5m   # a longer local pass
fuzz seconds="20s":
    go test ./pkg/net/udpproto -run '^$' -fuzz FuzzUDPProtoDecode -fuzztime {{seconds}}
    go test ./pkg/net/udpcrypto -run '^$' -fuzz FuzzSessionOpen -fuzztime {{seconds}}
    go test ./pkg/net/udpcrypto -run '^$' -fuzz FuzzReplayWindow -fuzztime {{seconds}}
    go test ./pkg/universe -run '^$' -fuzz FuzzUnmarshalTransferFrame -fuzztime {{seconds}}
    go test ./pkg/universe -run '^$' -fuzz FuzzDecodeTypedOpFrame -fuzztime {{seconds}}
    go test ./pkg/universe -run '^$' -fuzz FuzzDispatchInboundEventFrame -fuzztime {{seconds}}
    go test ./pkg/universe -run '^$' -fuzz FuzzDecodeMeshFrame -fuzztime {{seconds}}
    go test ./pkg/universe -run '^$' -fuzz FuzzReflectUnmarshal -fuzztime {{seconds}}

# regenerate the committed testdata/fuzz seed corpora from the builders in
# pkg/universe/fuzz_test.go and pkg/net/udpproto/fuzz_test.go. TestFuzzSeedCorpus
# asserts the committed files still match, so run this after changing a seed.
fuzz-corpus:
    go test ./pkg/universe ./pkg/net/udpproto -run TestFuzzSeedCorpus -count=1 -update-fuzz-corpus

# run the C# SDK core unit + golden tests
csharp-test:
    cd csharp && dotnet test

# regenerate the C# golden manifest from Go (authoritative wire bytes)
csharp-golden:
    go run ./cmd/csharp-golden

# run the shared TS core unit/golden tests (bun)
ts-core-test:
    bun test pkg/quantize/ts/

# every example's web-client TS suite (bun). These are the client-prediction,
# reconciliation and interpolation tests, which ts-core-test does not cover.
web-test:
    bun test examples/space/web/src/__tests__/ examples/4node-basic/web/src/__tests__/

# every TypeScript suite in the repo
client-test: ts-core-test web-test

# compile-gate the generated C# SDK (emits a sample SDK + dotnet build)
csharp-compile-test:
    go test -tags=csharptest ./cmd/sdkgen/ -run TestCsharpSdk_Compiles -v

# Target Unity Assets tree for the generated C# SDK. Deliberately has no
# default: the two recipes below fail with an explanation when it is unset.
unity_sdk_dir := env('UNITY_SDK_DIR', '')

# generate + deploy the C# client SDK for 4node-basic into the Unity Assets
# tree. UNITY_SDK_DIR is REQUIRED and has no default — it used to default to one
# developer's absolute /mnt/c path, which silently wrote the SDK somewhere
# meaningless (or nowhere) for everyone else.
# Control/admin listeners are disabled so the schema dump works even while a
# dev server is running. Requires Postgres (just db-up).
csharp-sdk:
    @test -n "{{ unity_sdk_dir }}" || { echo "error: UNITY_SDK_DIR is required. Set it to your Unity SDK target, e.g. UNITY_SDK_DIR=/path/to/UnityProject/Assets/Mmokit/Sdk just csharp-sdk" >&2; exit 1; }
    go run ./examples/4node-basic --dump-schema --control-listen= --admin-listen= \
        "--postgres-url={{ env('POSTGRES_URL', 'postgres://mmo:mmo@localhost:5432/mmo_4node?sslmode=disable') }}" \
      | go run ./cmd/sdkgen --lang=csharp \
          --csharp-core csharp/Mmokit.Sdk.Core \
          --out "{{ unity_sdk_dir }}"

# headless C# smoke-bot: live UDP round-trip (connect → AuthLogin → OnWorldDelta)
# against a RUNNING 4node server. Compiles the generated SDK from UNITY_SDK_DIR.
# Args: just csharp-smoke [host] [username] [password] [seconds]   (defaults:
# 127.0.0.1 smokebot 4node-demo-password 12). For WSL2→Windows use the WSL IP.
# The UDP transport is experimental. The SERVER default is off, but the dev
# recipes enable it: `just dev` / `just distributed` in examples/4node-basic
# both pass --udp-listen=:9000. Launching the binary by hand does not.
# In distributed mode only the GATEWAY binds UDP.
# UNITY_SDK_DIR is REQUIRED and has no default (see csharp-sdk).
csharp-smoke *ARGS:
    @test -n "{{ unity_sdk_dir }}" || { echo "error: UNITY_SDK_DIR is required. Set it to the Unity SDK tree that 'just csharp-sdk' wrote, e.g. UNITY_SDK_DIR=/path/to/UnityProject/Assets/Mmokit/Sdk just csharp-smoke" >&2; exit 1; }
    dotnet run --project csharp/Mmokit.Sdk.SmokeBot \
        -p:UnitySdkDir="{{ unity_sdk_dir }}" \
        -- {{ARGS}}
