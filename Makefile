.PHONY: test test-contracts test-go test-go-pure check setup-test-tools build build-linux

setup-test-tools:
	uv sync --frozen

test: test-contracts test-go

test-contracts:
	uv run --frozen python -m unittest discover -s tests -v

test-go:
	go -C server test -race -count=1 ./...

test-go-pure:
	CGO_ENABLED=0 go -C server test -count=1 ./...

check: test
	go -C server vet ./...
	go -C server mod verify

build:
	mkdir -p bin
	CGO_ENABLED=0 go -C server build -trimpath -o ../bin/homebox ./cmd/homebox

build-linux:
	mkdir -p bin
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go -C server build -trimpath -o ../bin/homebox-linux-amd64 ./cmd/homebox
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go -C server build -trimpath -o ../bin/homebox-linux-arm64 ./cmd/homebox
