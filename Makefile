.PHONY: build run test test-integration test-systemtest vet docker sandbox-image systemtest-sandbox-image dev-db lint kind-create kind-load kind-deploy kind-smee kind-delete

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

SYSTEMTEST_MODEL ?= qwen2.5:0.5b

systemtest-sandbox-image:
	docker build -t ai-coworker-systemtest-sandbox:latest -f test/systemtest/sandbox/Dockerfile test/systemtest/sandbox/

test-systemtest: systemtest-sandbox-image
	@echo "Ensuring Ollama model $(SYSTEMTEST_MODEL) is available..."
	ollama pull $(SYSTEMTEST_MODEL)
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
