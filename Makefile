.PHONY: test test-contracts setup-test-tools

setup-test-tools:
	uv sync --frozen

test: test-contracts

test-contracts:
	uv run --frozen python -m unittest discover -s tests -v
