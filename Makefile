.PHONY: build run test docker sandbox-image dev-db lint

build:
	go build -o ai-coworker ./cmd/ai-coworker/

run: build
	./ai-coworker

test:
	go test ./...

docker:
	docker build -t ai-coworker:latest .

sandbox-image:
	docker build -t ai-coworker-sandbox:latest -f sandbox/Dockerfile sandbox/

dev-db:
	docker compose up -d postgres

lint:
	golangci-lint run ./...
