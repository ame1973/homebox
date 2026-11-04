.PHONY: setup-test-tools setup-web test test-contracts test-go test-go-pure test-web test-e2e check check-backend check-web audit-web build build-web build-linux

setup-test-tools:
	uv sync --frozen

setup-web:
	npm ci --prefix web --ignore-scripts

test: test-contracts test-go test-web

test-contracts:
	uv run --frozen python -m unittest discover -s tests -v

test-go:
	go -C server test -race -count=1 ./...

test-go-pure:
	CGO_ENABLED=0 go -C server test -count=1 ./...

test-web:
	npm --prefix web test

test-e2e: build-web
	npm --prefix web run test:e2e

check: check-backend check-web

check-backend: test-contracts test-go
	go -C server vet ./...
	go -C server mod verify

check-web: test-web
	npm --prefix web run api:check
	npm --prefix web run lint
	npm --prefix web run test:build
	npm --prefix web run build

audit-web:
	npm --prefix web audit --omit=dev --audit-level=low
	npm --prefix web audit --audit-level=high

build-web:
	npm --prefix web run build

build:
	mkdir -p bin
	CGO_ENABLED=0 go -C server build -trimpath -o ../bin/homebox ./cmd/homebox

build-linux:
	mkdir -p bin
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go -C server build -trimpath -o ../bin/homebox-linux-amd64 ./cmd/homebox
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go -C server build -trimpath -o ../bin/homebox-linux-arm64 ./cmd/homebox
