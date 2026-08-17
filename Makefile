.PHONY: build check clean fmt install run test

build:
	mkdir -p bin
	go build -trimpath -ldflags "-s -w" -o bin/gack ./cmd/gack

check: fmt
	git diff --exit-code
	go vet ./...
	go test ./...

clean:
	rm -f bin/gack

fmt:
	gofmt -w cmd internal

install:
	go install -trimpath ./cmd/gack

run:
	go run ./cmd/gack --demo

test:
	go test ./...
