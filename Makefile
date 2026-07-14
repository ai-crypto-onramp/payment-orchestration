.PHONY: build test run lint docker-build docker-run clean

build:
	go build -o bin/payment-orchestration ./cmd/payment-orchestration

test:
	go test ./cmd/... ./internal/... -race -coverprofile=coverage.out -coverpkg=./cmd/...,./internal/...

run:
	go run ./cmd/payment-orchestration

lint:
	golangci-lint run

docker-build:
	docker build -t ai-crypto-onramp/payment-orchestration .

docker-run:
	docker run --rm -p 8080:8080 ai-crypto-onramp/payment-orchestration

clean:
	rm -rf bin/ coverage.out
