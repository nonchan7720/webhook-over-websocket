# syntax=docker/dockerfile:1@sha256:572cebbc2b15e6f84c077742952674889cb63cd5e9b80abdffcbe587b6f19183
FROM --platform=$BUILDPLATFORM golang:1.27.4-alpine@sha256:70bd7ddea8b6cbe5d6943cde279d6b67feb10cc924fb9100d1d8757c1575b280 AS app-builder

ARG TARGETPLATFORM
ARG TARGETARCH

ENV GO111MODULE=on \
  GOPATH=/go \
  GOBIN=/go/bin \
  GOARCH=${TARGETARCH}

WORKDIR /workspace

COPY go.mod go.sum main.go ./
COPY pkg pkg
RUN --mount=type=cache,target=/go/pkg/mod \
  --mount=type=cache,target=/root/.cache/go-build \
  CGO_ENABLED=0 go build -o webhook-over-websocket \
  && chmod +x /workspace/webhook-over-websocket

FROM gcr.io/distroless/static:nonroot@sha256:18f4cc26c9ffa7f20a359298be7deadac5c607c8e03a02b8119f1a5fc2f82c99
ENV TZ=Asia/Tokyo

COPY --from=app-builder --chown=nonroot:nonroot /workspace/webhook-over-websocket /usr/local/bin/webhook-over-websocket

CMD [ "webhook-over-websocket" ]
