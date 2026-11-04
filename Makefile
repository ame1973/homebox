.PHONY: test test-contracts test-go test-go-pure check setup-test-tools

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
