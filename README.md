# webhook-over-websocket

[![Release](https://img.shields.io/github/v/release/nonchan7720/webhook-over-websocket)](https://github.com/nonchan7720/webhook-over-websocket/releases)
[![Go Version](https://img.shields.io/github/go-mod/go-version/nonchan7720/webhook-over-websocket)](go.mod)
[![License](https://img.shields.io/github/license/nonchan7720/webhook-over-websocket)](LICENSE)

A tunnel tool that forwards external webhook requests to a local development server via WebSocket.

## Overview

`webhook-over-websocket` allows you to receive webhooks from external services (e.g. GitHub, Stripe, Slack) on your local development machine without exposing it to the internet directly. It works by establishing a persistent WebSocket connection between a publicly accessible server and the client running locally.

```
External Service → (HTTP) → Server /webhook/{channel_id}
                                 ↕ WebSocket
                             Client (local machine)
                                 ↓ (HTTP)
                          Local application (e.g. http://localhost:3000)
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
| `GET /traefik-config`              | Returns dynamic Traefik routing configuration (HTTP Provider)                         |
| `GET /internal/channels`           | Returns active channel list (used for peer-to-peer sync in multi-replica deployments) |
| `GET /ws/{channel_id}`             | WebSocket upgrade endpoint for client connections                                     |
| `POST /webhook/{channel_id}[/...]` | Receives external webhook requests and tunnels them to the client                     |

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

| Flag                         | Default   | Description                                        |
| ---------------------------- | --------- | -------------------------------------------------- |
| `--port`, `-p`               | `8080`    | Port to listen on                                  |
| `--peer-domain`              | *(empty)* | Peer domain name for memberlist cluster discovery  |
| `--cleanup-duration`         | `5m`      | Interval for cleaning up inactive channel sessions |
| `--memberlist-port`          | `7946`    | Port for memberlist gossip protocol                |
| `--memberlist-sync-duration` | `5s`      | Interval for memberlist cluster synchronization    |

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

| Flag           | Default                 | Description                                                 |
| -------------- | ----------------------- | ----------------------------------------------------------- |
| `--server-url` | *(required)*            | URL of the webhook-over-websocket server                    |
| `--target-url` | `http://localhost:3000` | URL of the local application to forward webhook requests to |

### 3. Configure the external service

Set the webhook URL in the external service (e.g. GitHub, Stripe) to:

```
http://your-server.example.com/webhook/<channel_id>
```

Any path suffix after the channel ID is preserved and forwarded to your local application as-is.

## Environment Variables

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

## Development

The repository includes a Docker Compose file for local development:

```bash
docker compose up -d
```

This mounts the repository source into the container so you can edit files locally.
