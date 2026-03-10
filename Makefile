.PHONY: build run proto clean dev web-install web-build dist

build:
	go build -o bin/server ./cmd/server

run: build
	./bin/server

proto:
	buf generate

clean:
	rm -rf bin/ web/dist/

web-install:
	cd web && bun install

web-build: web-install
	cd web && bunx vite build

dev: build
	cd web && bunx vite &>/dev/null & VITE_PID=$$!; \
	trap "kill $$VITE_PID 2>/dev/null" EXIT; \
	./bin/server

dist: build web-build
