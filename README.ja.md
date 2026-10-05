<div align="center">
  <img src="docs/logo.png" alt="One Gateway ロゴ" width="120" />

  # One Gateway

  **すべてのモデルプロバイダーに、ひとつの OpenAI 互換エンドポイントを。**

  [![CI](https://github.com/infinmalum/one-gateway/actions/workflows/ci.yml/badge.svg)](https://github.com/infinmalum/one-gateway/actions/workflows/ci.yml)
  [![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](LICENSE)
  ![Go](https://img.shields.io/badge/Go-1.27-00ADD8?logo=go&logoColor=white)
  ![Node.js](https://img.shields.io/badge/Node.js-24-339933?logo=nodedotjs&logoColor=white)

  [English](README.md) · [简体中文](README.zh-CN.md) · 日本語
</div>

---

One Gateway は [One API](https://github.com/songquanpeng/one-api) のコミュニティフォークです。複数のモデルプロバイダーに対して OpenAI 互換の共通エンドポイントを提供し、チャネル、ユーザー、トークン、利用枠、利用ログを Web 画面で管理できます。元の One API は JustSong 氏と他の貢献者によって開発・保守されており、このフォークはその成果を基にしています。

> [!NOTE]
> このリポジトリは独立したフォークです。元プロジェクトのリリース、Docker イメージ、デモサイト、Issue は One Gateway のリリースやサポート窓口ではありません。

## 主な特徴

- 🌉 **ひとつのエンドポイント、複数のプロバイダー** — chat・completions・embeddings・画像・音声・バックグラウンド Responses を共通の `/v1` API で提供。
- 🔀 **スマートルーティング** — チャネルの負荷分散、モデル名マッピング、グループ別倍率、最初のバイトがクライアントに届く前の自動リトライ。
- 🧮 **利用枠と課金** — トークンごとの利用枠、利用ログ、引換コード、すべてのリクエストを倍率に基づいて正確に精算。
- 🖥️ **Web コンソール** — チャネル・ユーザー・トークン・ログを一画面で管理。
- 💾 **柔軟なストレージ** — 初期状態で SQLite、共有ストレージに MySQL / PostgreSQL、キャッシュと同期には Redis を利用可能。
- 🛠️ **モダンな技術スタック** — Go バックエンドと TypeScript + Vite フロントエンド。フロントエンドのビルド前には必ず型チェックを実行。

対応モデルと機能はチャネルの実装によって異なります。本番運用前に設定画面で確認し、対象モデルをテストしてください。

## Docker ですぐに起動する

リポジトリのルートで、このフォークのイメージをビルドします。

```sh
git clone https://github.com/infinmalum/one-gateway.git
cd one-gateway
docker build -t one-gateway:local .
docker run -d --name one-gateway --restart unless-stopped -p 3000:3000 -v "$(pwd)/data:/data" one-gateway:local
```

<http://localhost:3000> を開いてください。新しいデータベースでは、ユーザー名 `root`、パスワード `123456` のアカウントが作成されます。**初回ログイン後、直ちにパスワードを変更してください。**マウントしたディレクトリには標準の SQLite データベースが保存されるため、コンテナを更新しても保持してください。外部公開する場合は HTTPS と固定の `SESSION_SECRET` も設定してください。

同梱の `docker-compose.yml` はこのフォークをローカルでビルドし、イメージに `one-gateway:local` タグを付けます。既存の環境との互換性のため、データベース名とデータの保存先は変更していません。

## ソースからビルドする

Dockerfile は Node.js 24 と Go 1.27.1 を使用します。対応する環境でフロントエンドを先にビルドし、`web/build` を埋め込む Go バイナリを作成します。

```sh
npm ci --legacy-peer-deps --prefix web/default
npm run build --prefix web/default
go build -o one-gateway .
./one-gateway --port 3000
```

ダウンロードした npm と Go のモジュールは、それぞれ標準の永続キャッシュを使用します。フロントエンドの依存関係は `web/default/node_modules` に入り、成果物は `web/build/default` に出力されます。

## 設定と使い方

起動前に環境変数を設定できます。主な設定は次のとおりです。

| 環境変数 | 用途 |
| --- | --- |
| `PORT` | HTTP ポート。既定値は `3000`。 |
| `SQL_DSN` | MySQL DSN または `postgres://` URL。未設定なら SQLite。 |
| `SQLITE_PATH` | SQLite 使用時のデータベースファイルのパス。 |
| `SESSION_SECRET` | 固定のセッションシークレット。再起動やマルチノード構成では特に重要。 |
| `REDIS_CONN_STRING` | キャッシュ用の Redis 接続 URL。 |
| `SYNC_FREQUENCY` | キャッシュ同期の間隔（秒）。 |
| `NODE_TYPE` | マルチノード構成での `master` または `slave`。 |
| `FRONTEND_BASE_URL` | スレーブノードのフロントエンドリダイレクト先（任意）。 |

マルチノード構成では、すべてのノードを同じ MySQL / PostgreSQL データベースに接続し、同じ `SESSION_SECRET` を共有して、ノードの役割とキャッシュ同期を設定してください。設定の正確な一覧は [`common/config`](common/config) のコードと、元プロジェクトドキュメントの環境変数の節を参照してください。

ログイン後、**チャネル** ページでプロバイダーのキーを登録し、**トークン** ページでアクセストークンを作成します。OpenAI 互換クライアントのエンドポイントを `http://localhost:3000/v1` に設定し、作成したトークンを API キーとして指定してください：

```sh
curl http://localhost:3000/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -H 'Authorization: Bearer YOUR_TOKEN' \
  -d '{"model": "gpt-4o", "messages": [{"role": "user", "content": "Hello!"}]}'
```

## ドキュメント

- [管理 API リファレンス](docs/API.md)
- [リレーアーキテクチャの解説](docs/relay-rewrite-plan.md)
- [フロントエンドテーマの説明](web/README.md)
- [TypeScript 移行メモ](docs/typescript-migration.md)

## 開発

- フロントエンド編集中は `npm run typecheck --prefix web/default` を実行できます。
- 埋め込みアセットを変更する前に、`npm run build --prefix web/default` を実行してください。
- バックエンドの変更には `go test ./...` を使用します。

フォーク固有の不具合や変更は[このリポジトリの Issue](https://github.com/infinmalum/one-gateway/issues) で報告してください。報告前に、その挙動が元プロジェクトにも存在するか確認をお願いします。

## 由来とライセンス

One Gateway は [One API](https://github.com/songquanpeng/one-api) から派生したプロジェクトで、原著作者は © 2023 JustSong および他の貢献者です。元プロジェクトの MIT ライセンスと表示は [`LICENSE.upstream`](LICENSE.upstream) に保存されています。このフォークの独自の変更は [Apache License 2.0](LICENSE) の下で提供され、帰属と適用範囲は [`NOTICE`](NOTICE) を参照してください。各ソースファイルおよびサードパーティ依存関係の元のライセンス表示は引き続き適用されます。
