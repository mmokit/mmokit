ROOT := $(shell git rev-parse --show-toplevel)

.PHONY: build run dev proto clean

build:
	cd $(ROOT) && go build -o bin/slither ./examples/slither

run: build
	cd $(ROOT) && ./bin/slither

dev: build
	set -m; cd web && bun run dev &>/dev/null & VITE_PID=$$!; \
	trap 'kill -- -$$VITE_PID 2>/dev/null; wait 2>/dev/null' INT TERM EXIT; \
	cd $(ROOT) && ./bin/slither

proto:
	cd proto && buf generate

clean:
	rm -f $(ROOT)/bin/slither
