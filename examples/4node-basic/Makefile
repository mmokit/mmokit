ROOT := $(shell git rev-parse --show-toplevel)

.PHONY: build run dev clean

build:
	cd $(ROOT) && go build -o bin/4node-basic ./examples/4node-basic

run: build
	cd $(ROOT) && ./bin/4node-basic

dev: build
	set -m; cd web && bun run dev &>/dev/null & VITE_PID=$$!; \
	trap 'kill -- -$$VITE_PID 2>/dev/null; wait 2>/dev/null' INT TERM EXIT; \
	cd $(ROOT) && ./bin/4node-basic

clean:
	rm -f $(ROOT)/bin/4node-basic
