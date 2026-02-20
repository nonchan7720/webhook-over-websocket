# webhook-over-websocket

[![Release](https://img.shields.io/github/v/release/nonchan7720/webhook-over-websocket)](https://github.com/nonchan7720/webhook-over-websocket/releases)
[![Go Version](https://img.shields.io/github/go-mod/go-version/nonchan7720/webhook-over-websocket)](go.mod)
[![License](https://img.shields.io/github/license/nonchan7720/webhook-over-websocket)](LICENSE)

[English](README.md)

WebSocket を介して外部の Webhook リクエストをローカル開発サーバーに転送するトンネルツールです。

## 概要

`webhook-over-websocket` を使うと、ローカルマシンをインターネットに直接公開することなく、外部サービス（GitHub、Stripe、Slack など）からの Webhook をローカル開発環境で受け取ることができます。公開アクセス可能なサーバーとローカルで動作するクライアントの間に永続的な WebSocket 接続を確立することで実現します。

```
外部サービス → (HTTP) → サーバー /webhook/{channel_id}
                                 ↕ WebSocket
                             クライアント（ローカルマシン）
                                 ↓ (HTTP)
                          ローカルアプリケーション（例: http://localhost:3000）
```

## アーキテクチャ

| コンポーネント | 役割                                                                                                                                                                                    |
| -------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **サーバー**   | 公開アクセス可能な HTTP サーバー。Webhook を受け取り、接続されているクライアントへ WebSocket 経由で転送します。スケールアウト時の動的ルーティング用に Traefik HTTP Provider エンドポイントも公開します。 |
| **クライアント** | ローカルマシン上で動作します。WebSocket でサーバーに接続し、Webhook ペイロードを受け取ってローカルアプリケーションに転送します。                                                          |

### サーバーエンドポイント

| エンドポイント                     | 説明                                                                                              |
| ---------------------------------- | ------------------------------------------------------------------------------------------------- |
| `GET /new`                         | クライアントが使用する新しい `channel_id`（UUID）を発行します                                      |
| `GET /traefik-config`              | Traefik の動的ルーティング設定（HTTP Provider）を返します                                          |
| `GET /internal/channels`           | アクティブなチャンネル一覧を返します（マルチレプリカ構成でのピア間同期に使用）                    |
| `GET /ws/{channel_id}`             | クライアント接続用の WebSocket アップグレードエンドポイント                                        |
| `POST /webhook/{channel_id}[/...]` | 外部からの Webhook リクエストを受け取り、クライアントにトンネリングします                          |

## インストール

### Docker

```bash
docker pull ghcr.io/nonchan7720/webhook-over-websocket:latest
```

### Go install

```bash
go install github.com/nonchan7720/webhook-over-websocket@latest
```

### バイナリダウンロード

[Releases](https://github.com/nonchan7720/webhook-over-websocket/releases) ページからお使いのプラットフォーム向けの最新バイナリをダウンロードしてください。

## 使い方

### 1. サーバーを起動する

公開アクセス可能なホスト上でサーバーを起動します：

```bash
webhook-over-websocket server --port 8080
```

Docker を使う場合：

```bash
docker run --rm -p 8080:8080 ghcr.io/nonchan7720/webhook-over-websocket:latest server --port 8080
```

**サーバーフラグ:**

| フラグ                         | デフォルト | 説明                                                   |
| ------------------------------ | ---------- | ------------------------------------------------------ |
| `--port`, `-p`                 | `8080`     | リッスンするポート番号                                  |
| `--peer-domain`                | *(空)*     | memberlist クラスター探索用のピアドメイン名             |
| `--cleanup-duration`           | `5m`       | 非アクティブなチャンネルセッションのクリーンアップ間隔  |
| `--memberlist-port`            | `7946`     | memberlist ゴシッププロトコル用ポート                   |
| `--memberlist-sync-duration`   | `5s`       | memberlist クラスター同期の間隔                         |

### 2. クライアントを起動する

ローカルマシン上でクライアントを起動し、サーバーとローカルアプリケーションを指定します：

```bash
webhook-over-websocket client \
  --server-url http://your-server.example.com \
  --target-url http://localhost:3000
```

起動時に、外部サービスへ設定する Webhook URL が表示されます：

```
Issued Channel ID: xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx
Please set the webhook destination as follows: http://your-server.example.com/webhook/xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx
A tunnel to the server has been established.
```

**クライアントフラグ:**

| フラグ           | デフォルト              | 説明                                                        |
| ---------------- | ----------------------- | ----------------------------------------------------------- |
| `--server-url`   | *(必須)*                | webhook-over-websocket サーバーの URL                        |
| `--target-url`   | `http://localhost:3000` | Webhook リクエストを転送するローカルアプリケーションの URL   |

### 3. 外部サービスを設定する

外部サービス（GitHub、Stripe など）の Webhook URL に以下を設定してください：

```
http://your-server.example.com/webhook/<channel_id>
```

チャンネル ID 以降のパスサフィックスはそのままローカルアプリケーションへ転送されます。

## 環境変数

| 変数名   | 説明                                                                                                                                    |
| -------- | --------------------------------------------------------------------------------------------------------------------------------------- |
| `POD_IP` | サーバー自身の IP として使用する Pod の IP アドレス（Kubernetes 用）。有効な IPv4 アドレスが設定された場合、自動検出の代わりに使用されます。 |

## クラスタリングと高可用性

### Memberlist を使った Traefik 連携

Kubernetes など複数のサーバーレプリカでの本番環境では、Traefik をロードバランサーとして使用し、動的ルーティングにより Webhook リクエストが常に正しい WebSocket 接続を保持するレプリカへ転送されるようにします。

**課題:** Traefik の [HTTP Provider](https://doc.traefik.io/traefik/providers/http/) は単一のエンドポイント URL しかポーリングできません。マルチレプリカ環境では、異なるレプリカに接続されたチャンネルのルーティング情報を、単一のエンドポイントからどのように返すかが問題になります。

**解決策:** [HashiCorp Memberlist](https://github.com/hashicorp/memberlist) のゴシップベースのメンバーシッププロトコルを使ってクラスター連携を実現します。Traefik がどのレプリカの `/traefik-config` エンドポイントをポーリングしても、そのレプリカがすべてのクラスターメンバーからチャンネル情報を自動的に集約し、完全なルーティング設定を返します。

**仕組み:**

1. 各サーバーインスタンスは `--peer-domain` フラグによる DNS ベースのピア探索を使い memberlist クラスターに参加します
2. サーバーはゴシッププロトコルを通じてアクティブなチャンネル情報を定期的に交換します
3. Traefik がいずれかのレプリカの `/traefik-config` をポーリングすると、そのレプリカは：
   - 自身のアクティブなチャンネルを収集
   - 他のすべての生存クラスターメンバーの `/internal/channels` を照会
   - すべてのチャンネル情報を集約し、完全な Traefik ルーティング設定を生成して返す
4. 非アクティブまたは障害が発生したノードは自動的に検出され、クラスターから除外されます

**設定例:**

サーバー:
```bash
webhook-over-websocket server \
  --port 8080 \
  --peer-domain webhook-service.default.svc.cluster.local \
  --memberlist-port 7946 \
  --memberlist-sync-duration 5s
```

Traefik スタティック設定:
```yaml
providers:
  http:
    endpoint: "http://webhook-over-websocket-service/traefik-config"
    pollInterval: "5s"
```

この設定により、Traefik は（Kubernetes サービス経由で）任意のレプリカに問い合わせるだけで、クラスター全体のすべてのチャンネルのルーティング情報を受け取ることができます。

## 開発

リポジトリにはローカル開発用の Docker Compose ファイルが含まれています：

```bash
docker compose up -d
```

これにより、リポジトリのソースがコンテナにマウントされ、ローカルでファイルを編集できます。
