<div align="center">
  <img src="docs/logo.png" alt="One Gateway logo" width="120" />

  # One Gateway

  **One OpenAI-compatible endpoint for every model provider.**

  [![CI](https://github.com/infinmalum/one-gateway/actions/workflows/ci.yml/badge.svg)](https://github.com/infinmalum/one-gateway/actions/workflows/ci.yml)
  [![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](LICENSE)
  ![Go](https://img.shields.io/badge/Go-1.27-00ADD8?logo=go&logoColor=white)
  ![Node.js](https://img.shields.io/badge/Node.js-24-339933?logo=nodedotjs&logoColor=white)

  English · [简体中文](README.zh-CN.md) · [日本語](README.ja.md)
</div>

---

One Gateway is a community fork of [One API](https://github.com/songquanpeng/one-api). It fronts many model providers with a single OpenAI-compatible API and ships a web console for channels, users, tokens, quotas, and usage logs. The upstream project is created and maintained by JustSong and its contributors; this fork builds on their work.

> [!NOTE]
> This repository is a separate fork. Upstream releases, Docker images, demo sites, and issue trackers are not releases or support channels for One Gateway.

## Highlights

- 🌉 **One endpoint, many providers** — a unified `/v1` API for chat, completions, embeddings, images, audio, and background Responses.
- 🔀 **Smart routing** — channel load balancing, model mappings, group rates, and automatic retries before the first byte reaches the client.
- 🧮 **Quota and billing** — per-token quotas, usage logs, redemption codes, and rate-based settlement for every request.
- 🖥️ **Web console** — manage channels, users, tokens, and logs in one place.
- 💾 **Flexible storage** — SQLite out of the box; MySQL or PostgreSQL for shared storage; Redis for caching and synchronization.
- 🛠️ **Modern stack** — a Go backend with TypeScript and Vite frontends; every frontend build runs its type check first.

Provider and model support varies by channel implementation. Check the channel settings and test a model before relying on it in production.

## Quick start with Docker

Build this fork's image from the repository root:

```sh
git clone https://github.com/infinmalum/one-gateway.git
cd one-gateway
docker build -t one-gateway:local .
docker run -d --name one-gateway --restart unless-stopped -p 3000:3000 -v "$(pwd)/data:/data" one-gateway:local
```

Open <http://localhost:3000>. On a new database, the application creates the `root` account with password `123456`. **Change that password immediately** in the console. The volume stores the default SQLite database; keep it when replacing the container. For an exposed deployment, configure HTTPS and a persistent `SESSION_SECRET` as well.

The checked-in `docker-compose.yml` builds this fork locally and tags the image `one-gateway:local`. Existing database names and data paths are retained for upgrades.

## Build from source

The Dockerfile uses Node.js 24 and Go 1.27.1. Install compatible versions, then build the frontend before compiling the Go binary, which embeds `web/build`:

```sh
npm ci --legacy-peer-deps --prefix web/default
npm run build --prefix web/default
go build -o one-gateway .
./one-gateway --port 3000
```

Downloaded npm and Go modules use their normal persistent caches. The frontend dependencies also live in `web/default/node_modules`; compiled assets go to `web/build/default`.

## Configure and use

Set environment variables before starting the server. Common settings are:

| Variable | Purpose |
| --- | --- |
| `PORT` | HTTP listening port; default `3000`. |
| `SQL_DSN` | MySQL DSN or `postgres://` URL. If unset, the server uses SQLite. |
| `SQLITE_PATH` | SQLite database path when using SQLite. |
| `SESSION_SECRET` | Stable secret for sessions, especially across restarts or replicas. |
| `REDIS_CONN_STRING` | Redis connection URL for cache use. |
| `SYNC_FREQUENCY` | Cache synchronization interval in seconds. |
| `NODE_TYPE` | `master` or `slave` for multi-node deployments. |
| `FRONTEND_BASE_URL` | Optional frontend redirect for a slave node. |

For multiple instances, point every node at the same MySQL or PostgreSQL database, use the same `SESSION_SECRET`, and configure node roles and cache synchronization. The source of truth for supported settings is [`common/config`](common/config) and the existing environment-variable section in the upstream project's documentation.

After signing in, add a provider key on **Channels**, then create an access token on **Tokens**. Point any OpenAI-compatible client at `http://localhost:3000/v1` and use that token as its API key:

```sh
curl http://localhost:3000/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -H 'Authorization: Bearer YOUR_TOKEN' \
  -d '{"model": "gpt-4o", "messages": [{"role": "user", "content": "Hello!"}]}'
```

## Documentation

- [Management API reference](docs/API.md)
- [Relay architecture notes](docs/relay-rewrite-plan.md)
- [Frontend theme notes](web/README.md)
- [TypeScript migration notes](docs/typescript-migration.md)

## Development

- Run `npm run typecheck --prefix web/default` while editing the frontend.
- Run `npm run build --prefix web/default` before changing embedded assets.
- Run `go test ./...` for backend changes.

Report fork-specific bugs and changes in [this repository's issues](https://github.com/infinmalum/one-gateway/issues). Please check whether a behavior also exists in the upstream project before filing it here.

## Origin and licenses

One Gateway is derived from [One API](https://github.com/songquanpeng/one-api), copyright © 2023 JustSong and other contributors. The upstream MIT license and notice are preserved in [`LICENSE.upstream`](LICENSE.upstream). Original changes made in this fork are offered under the [Apache License 2.0](LICENSE); see [`NOTICE`](NOTICE) for attribution and scope. Existing per-file and third-party license notices remain applicable.
