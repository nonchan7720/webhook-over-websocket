.PHONY: test lint

test: ## Run all tests with the race detector
	go test -race ./...

lint: ## Run golangci-lint
	golangci-lint run ./...
