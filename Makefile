.PHONY: build run test test-integration test-systemtest vet docker sandbox-image dev-db dev-systemtest lint kind-create kind-load kind-deploy kind-smee kind-delete

REGISTRY ?= ghcr.io/creydr
IMAGE ?= $(REGISTRY)/ai-coworker:latest
SANDBOX_IMAGE ?= $(REGISTRY)/ai-coworker-sandbox:latest

build:
	go build -o ai-coworker ./cmd/ai-coworker/

run: build
	./ai-coworker $(ARGS)

test:
	go test -race ./...

test-integration:
	go test -race -tags integration ./...

vet:
	go vet ./...

docker:
	docker build -t $(IMAGE) .

sandbox-image:
	docker build -t $(SANDBOX_IMAGE) -f sandbox/Dockerfile sandbox/

dev-db:
	docker compose up -d postgres

dev-systemtest:
	docker compose -f docker-compose.systemtest.yaml up -d
	@echo "Waiting for Ollama to be ready..."
	@until docker compose -f docker-compose.systemtest.yaml exec ollama ollama list >/dev/null 2>&1; do sleep 2; done
	docker compose -f docker-compose.systemtest.yaml exec ollama ollama pull qwen2.5:0.5b
	docker build -t ai-coworker-systemtest-sandbox:latest -f test/systemtest/sandbox/Dockerfile test/systemtest/sandbox/

test-systemtest:
	SYSTEMTEST_DATABASE_URL="postgres://ai_coworker:test@localhost:5433/ai_coworker_systemtest?sslmode=disable" \
	SYSTEMTEST_OLLAMA_URL="http://localhost:11434/v1" \
	go test -tags systemtest -timeout 300s -count=1 -v ./test/systemtest/...

lint:
	golangci-lint run ./...

kind-create:
	./hack/kind.sh --create

kind-load: docker sandbox-image
	IMAGE=$(IMAGE) SANDBOX_IMAGE=$(SANDBOX_IMAGE) ./hack/kind.sh --load

kind-deploy:
	./hack/kind.sh --deploy

kind-smee:
	./hack/kind.sh --smee

kind-delete:
	./hack/kind.sh --delete
