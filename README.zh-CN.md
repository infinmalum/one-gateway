<div align="center">
  <img src="docs/logo.png" alt="One Gateway 徽标" width="120" />

  # One Gateway

  **一个 OpenAI 兼容接口,连接所有模型服务商。**

  [![CI](https://github.com/infinmalum/one-gateway/actions/workflows/ci.yml/badge.svg)](https://github.com/infinmalum/one-gateway/actions/workflows/ci.yml)
  [![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](LICENSE)
  ![Go](https://img.shields.io/badge/Go-1.27-00ADD8?logo=go&logoColor=white)
  ![Node.js](https://img.shields.io/badge/Node.js-24-339933?logo=nodedotjs&logoColor=white)

  [English](README.md) · 简体中文 · [日本語](README.ja.md)
</div>

---

One Gateway 是 [One API](https://github.com/songquanpeng/one-api) 的社区派生项目。它用统一的 OpenAI 兼容接口连接多个模型服务，并提供渠道、用户、令牌、额度和用量日志的管理界面。原项目由 JustSong 及其他贡献者创建和维护；本项目建立在他们的工作之上。

> [!NOTE]
> 本仓库独立维护。原项目的版本、Docker 镜像、演示站点和 issue 列表不代表 One Gateway 的发布与支持渠道。

## 功能亮点

- 🌉 **一个接口，多家服务商** —— 统一的 `/v1` 接口，覆盖 chat、completions、embeddings、图像、音频和后台 Responses。
- 🔀 **智能路由** —— 渠道负载均衡、模型映射、分组倍率，以及首字节返回前的自动重试。
- 🧮 **额度与计费** —— 令牌额度、用量日志、兑换码，每个请求都按倍率精确结算。
- 🖥️ **管理控制台** —— 渠道、用户、令牌、日志一站管理。
- 💾 **灵活存储** —— 开箱即用 SQLite；共享存储支持 MySQL / PostgreSQL；缓存与同步可配合 Redis。
- 🛠️ **现代技术栈** —— Go 后端搭配 TypeScript + Vite 前端，每次前端构建都会先做类型检查。

不同渠道对模型和功能的支持并不完全相同。正式使用前，请在渠道设置中确认并测试目标模型。

## 使用 Docker 快速启动

在本仓库根目录构建本项目的镜像：

```sh
git clone https://github.com/infinmalum/one-gateway.git
cd one-gateway
docker build -t one-gateway:local .
docker run -d --name one-gateway --restart unless-stopped -p 3000:3000 -v "$(pwd)/data:/data" one-gateway:local
```

打开 <http://localhost:3000>。数据库首次初始化时会创建 `root` 账号，密码为 `123456`。**首次登录后请立即修改密码。**挂载目录保存默认的 SQLite 数据库，更换容器时应保留该目录。对外提供服务时还应配置 HTTPS 和固定的 `SESSION_SECRET`。

仓库的 `docker-compose.yml` 会在本地构建本项目，并将镜像标记为 `one-gateway:local`。为兼容已有部署，数据库名称与数据目录保持不变。

## 从源码构建

Dockerfile 使用 Node.js 24 和 Go 1.27.1。安装兼容版本后，先构建前端，再编译嵌入 `web/build` 的 Go 程序：

```sh
npm ci --legacy-peer-deps --prefix web/default
npm run build --prefix web/default
go build -o one-gateway .
./one-gateway --port 3000
```

下载的 npm 和 Go 模块使用各自默认的持久缓存。前端依赖安装在 `web/default/node_modules` 中，构建产物位于 `web/build/default`。

## 配置与使用

启动服务前可设置环境变量。常用配置如下：

| 环境变量 | 用途 |
| --- | --- |
| `PORT` | HTTP 监听端口，默认 `3000`。 |
| `SQL_DSN` | MySQL DSN 或 `postgres://` 地址；不设置时使用 SQLite。 |
| `SQLITE_PATH` | 使用 SQLite 时的数据库文件路径。 |
| `SESSION_SECRET` | 固定会话密钥；重启或多节点部署时尤其需要。 |
| `REDIS_CONN_STRING` | Redis 连接地址，用于缓存。 |
| `SYNC_FREQUENCY` | 缓存同步间隔，单位为秒。 |
| `NODE_TYPE` | 多节点部署时的 `master` 或 `slave`。 |
| `FRONTEND_BASE_URL` | 从节点可选的前端跳转地址。 |

多节点部署时，所有节点应连接同一个 MySQL 或 PostgreSQL 数据库，使用相同的 `SESSION_SECRET`，并配置节点角色与缓存同步。具体可用配置以 [`common/config`](common/config) 中的代码为准；原项目文档也有更完整的环境变量说明。

登录后，在 **渠道** 页面填写模型服务商密钥，再在 **令牌** 页面创建访问令牌。将任意 OpenAI 兼容客户端的地址设为 `http://localhost:3000/v1`，并使用刚创建的令牌作为 API Key：

```sh
curl http://localhost:3000/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -H 'Authorization: Bearer YOUR_TOKEN' \
  -d '{"model": "gpt-4o", "messages": [{"role": "user", "content": "Hello!"}]}'
```

## 文档

- [管理接口参考](docs/API.md)
- [中继架构说明](docs/relay-rewrite-plan.md)
- [前端主题说明](web/README.md)
- [TypeScript 迁移说明](docs/typescript-migration.md)

## 开发

- 修改前端时，可运行 `npm run typecheck --prefix web/default`。
- 修改嵌入的前端资源前，运行 `npm run build --prefix web/default`。
- 修改后端时，运行 `go test ./...`。

请在[本仓库的 issue 列表](https://github.com/infinmalum/one-gateway/issues)反馈派生项目的问题与改动。反馈前可先确认问题是否也存在于原项目。

## 来源与许可证

One Gateway 派生自 [One API](https://github.com/songquanpeng/one-api)，原项目版权归 © 2023 JustSong 及其他贡献者所有。原项目的 MIT 许可及声明保存在 [`LICENSE.upstream`](LICENSE.upstream)。本派生项目的原创修改按 [Apache License 2.0](LICENSE) 提供；归属与适用范围见 [`NOTICE`](NOTICE)。各源码文件及第三方依赖原有的许可声明仍然适用。
