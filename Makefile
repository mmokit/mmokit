.PHONY: build run proto clean dev botclient resetdb freshdev

build:
	go build -o bin/server ./cmd/server

run: build
	./bin/server

proto:
	buf generate

botclient:
	go build -o bin/botclient ./cmd/botclient

clean:
	rm -rf bin/

dev: build
	trap 'kill 0' INT TERM EXIT; \
	(cd web-pixi && exec bunx vite) &>/dev/null & \
	./bin/server

resetdb:
	rm -f data/gameserver.db

freshdev: resetdb dev
