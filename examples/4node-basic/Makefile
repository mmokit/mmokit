ROOT := $(shell git rev-parse --show-toplevel)

.PHONY: sdk build run dev clean

sdk:
	cd $(ROOT) && make client-sdk GAME=examples/4node-basic

build:
	cd $(ROOT) && go build -o bin/4node-basic ./examples/4node-basic

run: build
	cd $(ROOT) && ./bin/4node-basic

ARGS ?=

dev: build
	@tmux kill-session -t 4node-vite 2>/dev/null || true
	@tmux new-session -d -s 4node-vite -c $(ROOT)/examples/4node-basic/web 'bun run dev'
	@trap 'tmux kill-session -t 4node-vite 2>/dev/null' INT TERM EXIT; \
	cd $(ROOT) && ./bin/4node-basic $(ARGS)

clean:
	rm -f $(ROOT)/bin/4node-basic
