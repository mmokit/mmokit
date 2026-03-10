.PHONY: build run proto clean

build:
	go build -o bin/server ./cmd/server

run: build
	./bin/server

proto:
	buf generate

clean:
	rm -rf bin/
