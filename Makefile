.PHONY: test lint e2e

test: ## Run all tests with the race detector
	go test -race ./...

lint: ## Run golangci-lint
	golangci-lint run ./...

e2e: ## Run end-to-end tests (Postman CLI) against real server/client processes
	mise exec -- ./tests/e2e/run.sh
