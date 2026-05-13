.PHONY: build run test test-integration test-systemtest vet docker sandbox-image systemtest-sandbox-image systemtest-registry systemtest-db systemtest-ollama dev-db lint kind-create kind-load kind-deploy kind-smee kind-delete

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

systemtest-ollama: ollama
	@curl -sf $(SYSTEMTEST_OLLAMA_URL)/models >/dev/null 2>&1 || { \
		echo "Starting Ollama server..."; \
		$(OLLAMA) serve & \
		sleep 5; \
	}
	@echo "Ensuring Ollama model $(SYSTEMTEST_MODEL) is available..."
	$(OLLAMA) pull $(SYSTEMTEST_MODEL)

test-systemtest: systemtest-db systemtest-sandbox-image systemtest-ollama
	SYSTEMTEST_DATABASE_URL=$(SYSTEMTEST_DATABASE_URL) SYSTEMTEST_OLLAMA_URL=$(SYSTEMTEST_OLLAMA_URL) SYSTEMTEST_MODEL=$(SYSTEMTEST_MODEL) SYSTEMTEST_SANDBOX_IMAGE=$(SYSTEMTEST_SANDBOX_IMAGE) go test -tags systemtest -timeout 1h -count=1 -v ./test/systemtest/...

lint:
	golangci-lint run ./...

## Tool Dependencies

LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p $(LOCALBIN)

OLLAMA_OS ?= $(shell uname -s | tr A-Z a-z)
OLLAMA_ARCH ?= $(shell uname -m)
ifeq ($(OLLAMA_ARCH),x86_64)
	OLLAMA_ARCH := amd64
else ifeq ($(OLLAMA_ARCH),aarch64)
	OLLAMA_ARCH := arm64
endif

OLLAMA ?= $(LOCALBIN)/ollama

.PHONY: ollama
ollama: $(OLLAMA)
$(OLLAMA): $(LOCALBIN)
ifeq ($(OLLAMA_OS),darwin)
	@[ -f $(OLLAMA) ] || { \
		set -e; \
		echo "Downloading ollama for $(OLLAMA_OS)..."; \
		curl -fSL https://github.com/ollama/ollama/releases/latest/download/ollama-darwin.tgz -o $(LOCALBIN)/ollama.tgz; \
		tar -xzf $(LOCALBIN)/ollama.tgz -C $(LOCALBIN) --strip-components=1 bin/ollama; \
		rm -f $(LOCALBIN)/ollama.tgz; \
	}
else
	@[ -f $(OLLAMA) ] || { \
		set -e; \
		echo "Downloading ollama for $(OLLAMA_OS)/$(OLLAMA_ARCH)..."; \
		curl -fSL https://github.com/ollama/ollama/releases/latest/download/ollama-$(OLLAMA_OS)-$(OLLAMA_ARCH).tar.zst -o $(LOCALBIN)/ollama.tar.zst; \
		tar --zstd -xf $(LOCALBIN)/ollama.tar.zst -C $(LOCALBIN) --strip-components=1 bin/ollama; \
		rm -f $(LOCALBIN)/ollama.tar.zst; \
	}
endif

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
