# syntax=docker/dockerfile:1@sha256:4edf897a3ffa55b89f906fc8cc78afdb3f1834cc9c7083565e611a8a7d5fe99e
FROM --platform=$BUILDPLATFORM golang:1.27.3-alpine@sha256:b8c9b82affbbb82a020bbcf998dad2ba9516944ac7e883524642a481a9505e9c AS app-builder

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
