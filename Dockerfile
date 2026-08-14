FROM --platform=$BUILDPLATFORM golang:1.26.5-alpine@sha256:0178a641fbb4858c5f1b48e34bdaabe0350a330a1b1149aabd498d0699ff5fb2 AS app-builder

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

FROM gcr.io/distroless/static:nonroot
ENV TZ=Asia/Tokyo

COPY --from=app-builder --chown=nonroot:nonroot /workspace/webhook-over-websocket /usr/local/bin/webhook-over-websocket

CMD [ "webhook-over-websocket" ]
