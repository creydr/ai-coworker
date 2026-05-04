.PHONY: build run test docker sandbox-image dev-db lint kind-create kind-load kind-deploy kind-smee kind-delete

KIND_CLUSTER_NAME ?= ai-coworker
KIND_NAMESPACE ?= ai-coworker
IMAGE ?= quay.io/creydr/ai-coworker:latest
SANDBOX_IMAGE ?= quay.io/creydr/ai-coworker-sandbox:latest

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

kind-create:
	kind create cluster --name $(KIND_CLUSTER_NAME)

kind-load: docker sandbox-image
	docker tag ai-coworker:latest $(IMAGE)
	docker tag ai-coworker-sandbox:latest $(SANDBOX_IMAGE)
	kind load docker-image $(IMAGE) $(SANDBOX_IMAGE) --name $(KIND_CLUSTER_NAME)

kind-deploy:
	kubectl apply -k deploy/kubernetes/overlays/with-postgres/
	kubectl -n $(KIND_NAMESPACE) create secret generic ai-coworker \
		--from-literal=AI_COWORKER__DATABASE__URL="postgres://ai-coworker:password@postgres:5432/ai-coworker?sslmode=disable" \
		--from-literal=AI_COWORKER__LLM__API_KEY="$${ANTHROPIC_API_KEY}" \
		--from-literal=AI_COWORKER__GITHUB__ENABLED="true" \
		--from-literal=AI_COWORKER__GITHUB__APP_ID="$${AI_COWORKER__GITHUB__APP_ID}" \
		--from-literal=AI_COWORKER__GITHUB__PRIVATE_KEY="$${AI_COWORKER__GITHUB__PRIVATE_KEY}" \
		--from-literal=AI_COWORKER__GITHUB__WEBHOOK_SECRET="$${AI_COWORKER__GITHUB__WEBHOOK_SECRET}" \
		--from-literal=AI_COWORKER__GITHUB__BOT_USERNAME="$${AI_COWORKER__GITHUB__BOT_USERNAME}" \
		--dry-run=client -o yaml | kubectl apply -f -
	kubectl -n $(KIND_NAMESPACE) rollout restart deployment/ai-coworker
	kubectl -n $(KIND_NAMESPACE) rollout status deployment/ai-coworker --timeout=120s

kind-smee:
	kubectl -n $(KIND_NAMESPACE) port-forward svc/ai-coworker 8080:8080 &
	smee -u $(SMEE_URL) -t http://localhost:8080/webhook/github

kind-delete:
	kind delete cluster --name $(KIND_CLUSTER_NAME)
