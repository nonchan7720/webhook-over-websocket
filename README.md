# webhook-over-websocket

[![Release](https://img.shields.io/github/v/release/nonchan7720/webhook-over-websocket)](https://github.com/nonchan7720/webhook-over-websocket/releases)
[![Go Version](https://img.shields.io/github/go-mod/go-version/nonchan7720/webhook-over-websocket)](go.mod)
[![License](https://img.shields.io/github/license/nonchan7720/webhook-over-websocket)](LICENSE)

[日本語](README_ja.md)

A tunnel tool that forwards external webhook requests to a local development server via WebSocket.

## Overview

`webhook-over-websocket` allows you to receive webhooks from external services (e.g. GitHub, Stripe, Slack) on your local development machine without exposing it to the internet directly. It works by establishing a persistent WebSocket connection between a publicly accessible server and the client running locally.

```mermaid
flowchart TD
    A[External Service] -- HTTP --> B["Server<br>/webhook/{channel_id}"]
    B <-->|WebSocket| C["Client<br>(local machine)"]
    C -- HTTP --> D["Local application<br>(e.g. http://localhost:3000)"]
```

## Architecture

| Component  | Role                                                                                                                                                                                                  |
| ---------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **Server** | Publicly accessible HTTP server. Receives webhooks and forwards them over WebSocket to the connected client. Also exposes a Traefik HTTP Provider endpoint for dynamic routing when running at scale. |
| **Client** | Runs on the local machine. Connects to the server via WebSocket, receives webhook payloads, and forwards them to the local application.                                                               |

### Server Endpoints

| Endpoint                           | Description                                                                           |
| ---------------------------------- | ------------------------------------------------------------------------------------- |
| `GET /new`                         | Issues a new `channel_id` (UUID) for a client to use                                  |
| `GET /traefik-config`              | Returns dynamic Traefik routing configuration (HTTP Provider). *Memberlist backend only* |
| `GET /internal/channels`           | Returns active channel list (used for peer-to-peer sync in multi-replica deployments). *Memberlist backend only* |
| `GET /ws/{channel_id}`             | WebSocket upgrade endpoint for client connections                                     |
| `ANY /webhook/{channel_id}[/...]`  | Receives external webhook requests (any HTTP method) and tunnels them to the client   |
| `GET /auth/login`                 | *(auth mode only)* Redirects to the GitHub OAuth consent page                         |
| `GET /auth/callback`               | *(auth mode only)* GitHub OAuth callback; returns a session JWT                       |

## Installation

### Docker

```bash
docker pull ghcr.io/nonchan7720/webhook-over-websocket:latest
```

### Go install

```bash
go install github.com/nonchan7720/webhook-over-websocket@latest
```

### Binary download

Download the latest binary for your platform from the [Releases](https://github.com/nonchan7720/webhook-over-websocket/releases) page.

## Usage

### 1. Start the server

Run the server on a publicly accessible host:

```bash
webhook-over-websocket server --port 8080
```

Or with Docker:

```bash
docker run --rm -p 8080:8080 ghcr.io/nonchan7720/webhook-over-websocket:latest server --port 8080
```

**Server flags:**

| Flag                         | Default   | Description                                                            |
| ---------------------------- | --------- | ---------------------------------------------------------------------- |
| `--port`, `-p`               | `8080`    | Port to listen on                                                      |
| `--cleanup-duration`         | `5m`      | Interval for cleaning up inactive channel sessions                     |
| `--cluster-backend`          | `memberlist` | How replicas share state: `memberlist` or `redis` (see [Clustering and High Availability](#clustering-and-high-availability)) |
| `--peer-domain`              | *(empty)* | *(memberlist)* Peer domain name for memberlist cluster discovery       |
| `--memberlist-port`          | `7946`    | *(memberlist)* Port for memberlist gossip protocol                     |
| `--memberlist-sync-duration` | `5s`      | *(memberlist)* Interval for memberlist cluster synchronization         |
| `--redis-url`                | *(empty)* | *(redis)* Redis URL, e.g. `redis://[user:password@]host:6379/0` or `rediss://...` for TLS (required with `--cluster-backend redis`) |
| `--redis-key-prefix`         | `webhook-over-websocket` | *(redis)* Prefix of the Redis keys and Pub/Sub channels |
| `--redis-heartbeat-duration` | `5s`      | *(redis)* Interval for refreshing the ownership of connected channels  |
| `--redis-cluster-mode`       | `false`   | *(redis)* Connect using the Redis Cluster protocol                     |
| `--redis-tls-ca-file`        | *(empty)* | *(redis)* CA certificate file to verify the Redis server (enables TLS) |
| `--redis-tls-insecure-skip-verify` | `false` | *(redis)* Skip verifying the Redis server certificate (enables TLS; for testing only) |
| `--github-client-id`         | *(empty)* | GitHub OAuth App client ID — **enables authentication when set**       |
| `--github-client-secret`     | *(empty)* | GitHub OAuth App client secret (required when `--github-client-id` is set) |
| `--github-org`               | *(empty)* | Required GitHub organization — only members are allowed access         |
| `--jwt-signing-key`               | *(empty)* | Secret key for signing JWT tokens (required when `--github-client-id` is set) |

### 2. Start the client

Run the client on your local machine, pointing it at the server and your local application:

```bash
webhook-over-websocket client \
  --server-url http://your-server.example.com \
  --target-url http://localhost:3000
```

On startup, the client prints the webhook URL to configure in the external service:

```
Issued Channel ID: xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx
Please set the webhook destination as follows: http://your-server.example.com/webhook/xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx
A tunnel to the server has been established.
```

**Client flags:**

| Flag              | Default                 | Description                                                          |
| ----------------- | ----------------------- | -------------------------------------------------------------------- |
| `--server-url`    | *(required)*            | URL of the webhook-over-websocket server                             |
| `--target-url`    | `http://localhost:3000` | URL of the local application to forward webhook requests to          |
| `--token`         | *(empty)*               | Session JWT token for authentication (required when server auth is enabled) |
| `--insecure`      | `false`                 | Skip TLS certificate verification                                    |

### 3. Configure the external service

Set the webhook URL in the external service (e.g. GitHub, Stripe) to:

```
http://your-server.example.com/webhook/<channel_id>
```

Any path suffix after the channel ID is preserved and forwarded to your local application as-is. Any HTTP method (GET, POST, PUT, PATCH, DELETE, etc.) is likewise forwarded unchanged, ngrok-like.

## Authentication

When the server is configured with a GitHub OAuth App, only authenticated users can obtain `channel_id`s and connect via WebSocket.  Two levels of access control are supported:

1. **Organization check** – only members of a specific GitHub organization may authenticate (enabled via `--github-org`).
2. **Per-channel token** – every `channel_id` is bound to a signed JWT whose `sub` (subject) claim equals the `channel_id`, so a token issued for one channel cannot be used for another.

### Setup

1. [Create a GitHub OAuth App](https://docs.github.com/en/apps/oauth-apps/building-oauth-apps/creating-an-oauth-app) and set the callback URL to `http://your-server.example.com/auth/callback`.
2. Start the server with the auth flags:

```bash
webhook-over-websocket server \
  --port 8080 \
  --github-client-id  <YOUR_CLIENT_ID> \
  --github-client-secret <YOUR_CLIENT_SECRET> \
  --github-org my-organization \
  --jwt-signing-key  <A_LONG_RANDOM_STRING>
```

### New server endpoints (auth mode only)

| Endpoint            | Description                                                          |
| ------------------- | -------------------------------------------------------------------- |
| `GET /auth/github`  | Redirects the user to the GitHub OAuth consent page                  |
| `GET /auth/callback`| GitHub calls this after the user approves; returns the session JWT   |



| Variable | Description                                                                                                                      |
| -------- | -------------------------------------------------------------------------------------------------------------------------------- |
| `POD_IP` | Pod IP address used as the server's own IP (Kubernetes). When set to a valid IPv4 address, it is used instead of auto-detection. |

## Clustering and High Availability

### Traefik Integration with Memberlist

For production deployments with multiple server replicas (e.g. in Kubernetes), Traefik is used as a load balancer with dynamic routing so that webhook requests are always forwarded to the replica that holds the correct WebSocket connection.

**Challenge:** Traefik's [HTTP Provider](https://doc.traefik.io/traefik/providers/http/) can only poll a single endpoint URL for configuration updates. In a multi-replica deployment, this creates a problem: how can a single endpoint return routing information for channels connected to different replicas?

**Solution:** [HashiCorp Memberlist](https://github.com/hashicorp/memberlist) enables cluster coordination via a gossip-based membership protocol. When Traefik polls any single replica's `/traefik-config` endpoint, that replica automatically aggregates channel information from all cluster members and returns the complete routing configuration.

**How it works:**

1. Each server instance joins the memberlist cluster using the `--peer-domain` flag for DNS-based peer discovery
2. Servers periodically exchange information about their active channels via the gossip protocol  
3. When Traefik polls `/traefik-config` on any replica, that replica:
   - Collects its own active channels
   - Queries all other alive cluster members via `/internal/channels`
   - Aggregates all channel information and generates the complete Traefik routing configuration
4. Inactive or failed nodes are automatically detected and removed from the cluster

**Configuration example:**

Server:
```bash
webhook-over-websocket server \
  --port 8080 \
  --peer-domain webhook-service.default.svc.cluster.local \
  --memberlist-port 7946 \
  --memberlist-sync-duration 5s
```

Traefik static configuration:
```yaml
providers:
  http:
    endpoint: "http://webhook-over-websocket-service/traefik-config"
    pollInterval: "5s"
```

With this setup, Traefik can query any single replica (via the Kubernetes service), and that replica will return routing information for all channels across the entire cluster.

### Redis Pub/Sub

The memberlist backend requires that the replicas can reach each other directly (gossip and `/internal/channels`) and that Traefik can route to every replica. When that is not possible — for example, replicas running in multiple clusters in an active-active setup — use the Redis backend instead. All traffic between replicas then goes through Redis, and neither Traefik nor memberlist is needed: a plain load balancer can send any request to any replica.

**How it works:**

1. `GET /new` registers the issued channel in Redis, so `/ws/{channel_id}` can be accepted by any replica.
2. The replica that accepts the WebSocket registers itself as the owner of the channel and keeps the ownership alive with a heartbeat (`--redis-heartbeat-duration`). The registration is removed when the client disconnects.
3. When a webhook arrives at a replica that does not hold the WebSocket, the request is published to the owner's channel in Redis. The owner sends it to the client, and the response (headers, body chunks and EOF) is published back and streamed to the caller.
4. Authentication results (`/auth/callback`) are broadcast to all replicas through Redis as well.

Messages between replicas use a compact binary format, and payloads larger than 1 KiB are compressed with zstd when it makes them smaller. This keeps the Pub/Sub output buffers in Redis small.

**Configuration example:**

```bash
webhook-over-websocket server \
  --port 8080 \
  --cluster-backend redis \
  --redis-url rediss://user:password@redis.example.com:6379/0
```

- Use `rediss://` (or `--redis-tls-ca-file`) for TLS. Managed Redis services with in-transit encryption work with `rediss://` as is.
- Use `--redis-cluster-mode` when Redis runs in cluster mode.
- `/traefik-config` and `/internal/channels` are not served with the Redis backend.

**Limitations:** Redis Pub/Sub delivers messages at most once. If the replica holding the WebSocket goes away while a webhook is in flight, the caller receives `504 Gateway Timeout`.

## Development

### Tooling with mise

Go and golangci-lint are declared in [`mise.toml`](mise.toml), so [mise](https://mise.jdx.dev/) installs the same versions CI uses:

```bash
mise install
make test   # go test -race ./...
make lint   # golangci-lint run ./...
make e2e    # end-to-end tests (tests/e2e) with the Postman CLI, for both the memberlist and Redis backends
```

CI (`.github/workflows/test.yaml`) sets up the tools from `mise.toml` as well, so bumping a version there updates both local and CI environments. Claude Code on the web runs `scripts/mise-setup.sh` on session start to install them.

### Docker Compose

The repository also includes a Docker Compose file for local development:

```bash
docker compose up -d
```

This mounts the repository source into the container so you can edit files locally.
