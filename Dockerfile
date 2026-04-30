FROM --platform=$BUILDPLATFORM golang:1.26-bookworm AS builder

ARG TARGETOS
ARG TARGETARCH

WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -o ai-coworker ./cmd/ai-coworker/

FROM gcr.io/distroless/static-debian12
COPY --from=builder /build/ai-coworker /ai-coworker
COPY --from=builder /build/config.yaml /config.yaml
ENTRYPOINT ["/ai-coworker"]
