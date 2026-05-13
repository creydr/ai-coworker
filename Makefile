.PHONY: build run test test-integration test-systemtest vet docker sandbox-image systemtest-sandbox-image systemtest-registry systemtest-db dev-db lint kind-create kind-load kind-deploy kind-smee kind-delete

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

systemtest-db:
	docker compose up -d postgres-systemtest

SYSTEMTEST_DATABASE_URL ?= postgres://ai_coworker:test@localhost:5433/ai_coworker_systemtest?sslmode=disable
SYSTEMTEST_OLLAMA_URL ?= http://localhost:11434/v1
SYSTEMTEST_MODEL ?= qwen3:1.7b
SYSTEMTEST_REGISTRY ?= localhost:5001
SYSTEMTEST_SANDBOX_IMAGE ?= $(SYSTEMTEST_REGISTRY)/ai-coworker-systemtest-sandbox:latest

systemtest-registry:
	@docker container inspect -f '{{.State.Running}}' systemtest-registry 2>/dev/null | grep -q true || docker run -d --name systemtest-registry -p 5001:5000 registry:2

systemtest-sandbox-image: systemtest-registry
	docker build -t $(SYSTEMTEST_SANDBOX_IMAGE) -f test/systemtest/sandbox/Dockerfile test/systemtest/sandbox/
	docker push $(SYSTEMTEST_SANDBOX_IMAGE)

test-systemtest: systemtest-db systemtest-sandbox-image
	@echo "Ensuring Ollama model $(SYSTEMTEST_MODEL) is available..."
	ollama pull $(SYSTEMTEST_MODEL)
	SYSTEMTEST_DATABASE_URL=$(SYSTEMTEST_DATABASE_URL) SYSTEMTEST_OLLAMA_URL=$(SYSTEMTEST_OLLAMA_URL) SYSTEMTEST_MODEL=$(SYSTEMTEST_MODEL) SYSTEMTEST_SANDBOX_IMAGE=$(SYSTEMTEST_SANDBOX_IMAGE) go test -tags systemtest -timeout 1h -count=1 -v ./test/systemtest/...

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
