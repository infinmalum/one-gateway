# One Gateway

[English](README.md) · 简体中文 · [日本語](README.ja.md)

One Gateway 是 [One API](https://github.com/songquanpeng/one-api) 的社区派生项目。它通过统一的 OpenAI 兼容接口连接多个模型服务，并提供渠道、用户、令牌、额度和用量日志的管理界面。原项目由 JustSong 及其他贡献者创建和维护；本项目建立在他们的工作之上。

> 本仓库独立维护。原项目的版本、Docker 镜像、演示站点和 issue 列表不代表 One Gateway 的发布与支持渠道。

## 功能概览

- 统一的 `/v1` 接口，可配置渠道、模型映射、分组倍率和失败重试。
- 支持流式请求、渠道负载均衡、额度统计、用量日志、访问令牌和兑换码。
- 提供 `default`、`air`、`berry` 三套前端主题。
- 单实例可用 SQLite；共享存储可用 MySQL 或 PostgreSQL。缓存及同步配置可配合 Redis 使用。
- 前端使用 TypeScript 和 Vite，后端使用 Go；前端构建会先运行类型检查。

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

Dockerfile 使用 Node.js 24 和 Go 1.27.1。安装兼容版本后，先构建三个前端主题，再编译嵌入 `web/build` 的 Go 程序：

```sh
npm ci --legacy-peer-deps --prefix web/default
npm ci --legacy-peer-deps --prefix web/air
npm ci --legacy-peer-deps --prefix web/berry
npm run build --prefix web/default
npm run build --prefix web/air
npm run build --prefix web/berry
go build -o one-gateway .
./one-gateway --port 3000
```

下载的 npm 和 Go 模块使用各自默认的持久缓存。前端依赖还会安装在各主题的 `node_modules` 中，构建产物位于 `web/build/<主题名>`。

## 配置与使用

启动服务前可设置环境变量。常用配置如下：

| 环境变量 | 用途 |
| --- | --- |
| `PORT` | HTTP 监听端口，默认 `3000`。 |
| `THEME` | `default`、`air` 或 `berry`，默认 `default`。 |
| `SQL_DSN` | MySQL DSN 或 `postgres://` 地址；不设置时使用 SQLite。 |
| `SQLITE_PATH` | 使用 SQLite 时的数据库文件路径。 |
| `SESSION_SECRET` | 固定会话密钥；重启或多节点部署时尤其需要。 |
| `REDIS_CONN_STRING` | Redis 连接地址，用于缓存。 |
| `SYNC_FREQUENCY` | 缓存同步间隔，单位为秒。 |
| `NODE_TYPE` | 多节点部署时的 `master` 或 `slave`。 |
| `FRONTEND_BASE_URL` | 从节点可选的前端跳转地址。 |

多节点部署时，所有节点应连接同一个 MySQL 或 PostgreSQL 数据库，使用相同的 `SESSION_SECRET`，并配置节点角色与缓存同步。具体可用配置以 [`common/config`](common/config) 中的代码为准；原项目文档也有更完整的环境变量说明。

登录后，在 **渠道** 页面填写模型服务商密钥，再在 **令牌** 页面创建访问令牌。将兼容 OpenAI 接口的客户端地址设为 `http://localhost:3000/v1`，并使用刚创建的令牌作为 API Key。例如：

```sh
curl http://localhost:3000/v1/models -H 'Authorization: Bearer YOUR_TOKEN'
```

其他管理接口见 [API 文档](docs/API.md)。可用模型取决于已配置的渠道。

## 开发

- 修改主题时，可运行 `npm run typecheck --prefix web/default`、`web/air` 或 `web/berry`。
- 修改嵌入的前端资源前，运行三个主题的 `npm run build --prefix web/<主题名>`。
- 修改后端时，运行 `go test ./...`。
- 参考[主题说明](web/README.md)和 [TypeScript 迁移说明](docs/typescript-migration.md)。

请在[本仓库的 issue 列表](https://github.com/infinmalum/one-gateway/issues)反馈派生项目的问题与改动。反馈前可先确认问题是否也存在于原项目。

## 来源与许可证

One Gateway 派生自 [One API](https://github.com/songquanpeng/one-api)，原项目版权归 © 2023 JustSong 及其他贡献者所有。原项目的 MIT 许可及声明保存在 [`LICENSE.upstream`](LICENSE.upstream)。本派生项目的原创修改按 [Apache License 2.0](LICENSE) 提供；归属与适用范围见 [`NOTICE`](NOTICE)。各源码文件及第三方依赖原有的许可声明仍然适用。
