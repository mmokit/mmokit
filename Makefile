.PHONY: build run proto clean dev

build:
	go build -o bin/server ./cmd/server

run: build
	./bin/server

proto:
	buf generate

clean:
	rm -rf bin/

dev: build
	trap 'kill 0' INT TERM EXIT; \
	(cd web-pixi && exec bunx vite) &>/dev/null & \
	./bin/server
