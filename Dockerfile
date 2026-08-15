FROM --platform=$BUILDPLATFORM golang:1.25.6-alpine AS app-builder

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

FROM gcr.io/distroless/static:nonroot@sha256:f7f8f729987ad0fdf6b05eeeae94b26e6a0f613bdf46feea7fc40f7bd72953e6
ENV TZ=Asia/Tokyo

COPY --from=app-builder --chown=nonroot:nonroot /workspace/webhook-over-websocket /usr/local/bin/webhook-over-websocket

CMD [ "webhook-over-websocket" ]
