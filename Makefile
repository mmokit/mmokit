.PHONY: build run proto clean dev botclient resetdb freshdev prometheus prometheus-stop prometheus-reload prometheus-restart client-sdk

build:
	go build -o bin/server ./cmd/server

run: build
	./bin/server

proto:
	buf generate

botclient:
	go build -o bin/botclient ./cmd/botclient

client-sdk:
	@test -n "$(GAME)" || (echo "Usage: make client-sdk GAME=examples/4node-basic" && exit 1)
	go run ./$(GAME) --dump-schema | go run ./cmd/sdkgen \
		--out $(GAME)/web/sdk \
		--proto-es gen/es \
		--core pkg/quantize/ts/delta-decoder-core.ts

clean:
	rm -rf bin/

dev: build
	set -m; cd web-pixi && bunx vite &>/dev/null & VITE_PID=$$!; \
	trap 'kill -- -$$VITE_PID 2>/dev/null; wait 2>/dev/null' INT TERM EXIT; \
	cd $(CURDIR) && ./bin/server

resetdb:
	rm -f data/gameserver.db
	rm -f data/marketplace.db

freshdev: resetdb dev

prometheus:
	@which prometheus >/dev/null 2>&1 || (echo "Install: sudo apt install prometheus  OR  brew install prometheus" && exit 1)
	prometheus --config.file=$(CURDIR)/prometheus.yml \
		--storage.tsdb.path=$(CURDIR)/data/prometheus \
		--web.listen-address=:9090 &
	@echo "Prometheus UI: http://localhost:9090"

prometheus-stop:
	@pkill -f 'prometheus.*config.file' 2>/dev/null && echo "Prometheus stopped" || echo "Prometheus not running"

prometheus-reload:
	@pkill -HUP -f 'prometheus.*config.file' 2>/dev/null && echo "Prometheus config reloaded" || echo "Prometheus not running"

prometheus-restart: prometheus-stop
	@sleep 1
	$(MAKE) prometheus
