# One Gateway

[English](README.md) · [简体中文](README.zh-CN.md) · 日本語

One Gateway は [One API](https://github.com/songquanpeng/one-api) のコミュニティフォークです。複数のモデルプロバイダーに対して OpenAI 互換の共通エンドポイントを提供し、チャネル、ユーザー、トークン、利用枠、利用ログを Web 画面で管理できます。元の One API は JustSong 氏と他の貢献者によって開発・保守されており、このフォークはその成果を基にしています。

> このリポジトリは独立したフォークです。元プロジェクトのリリース、Docker イメージ、デモサイト、Issue は One Gateway のリリースやサポート窓口ではありません。

## 主な機能

- 共通の `/v1` API、設定可能なプロバイダーチャネル、モデル名のマッピング、グループ別倍率、自動リトライ。
- ストリーミング、チャネルの負荷分散、利用枠とログの管理、アクセストークン、引換コード。
- `default`、`air`、`berry` の 3 種類の Web テーマ。
- 単一インスタンス向けの SQLite と、共有ストレージ向けの MySQL/PostgreSQL。キャッシュと同期の設定には Redis も利用できます。
- Go バックエンドと TypeScript/Vite フロントエンド。フロントエンドのビルド前に型チェックが実行されます。

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

同梱の `docker-compose.yml` はまだ元プロジェクトのイメージを参照しています。このフォークを実行する場合は上記のローカルビルドを利用してください。

## ソースからビルドする

Dockerfile は Node.js 24 と Go 1.27.1 を使用します。対応する環境で 3 つのテーマを先にビルドし、`web/build` を埋め込む Go バイナリを作成します。

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

ダウンロードした npm と Go のモジュールは、それぞれ標準の永続キャッシュを使用します。フロントエンドの依存関係は各テーマの `node_modules` に入り、成果物は `web/build/<テーマ名>` に出力されます。

## 設定と使い方

起動前に環境変数を設定できます。主な設定は次のとおりです。

| 環境変数 | 用途 |
| --- | --- |
| `PORT` | HTTP ポート。既定値は `3000`。 |
| `THEME` | `default`、`air`、`berry`。既定値は `default`。 |
| `SQL_DSN` | MySQL DSN または `postgres://` URL。未設定なら SQLite。 |
| `SQLITE_PATH` | SQLite 使用時のデータベースファイルのパス。 |
| `SESSION_SECRET` | 再起動や複数ノードで共通に使う固定のセッション秘密鍵。 |
| `REDIS_CONN_STRING` | キャッシュに使う Redis の接続先。 |
| `SYNC_FREQUENCY` | キャッシュ同期の間隔（秒）。 |
| `NODE_TYPE` | 複数ノード構成の `master` または `slave`。 |
| `FRONTEND_BASE_URL` | スレーブノードからの任意のフロントエンド転送先。 |

複数ノードでは、全ノードを同じ MySQL または PostgreSQL に接続し、同じ `SESSION_SECRET` と適切なノード・キャッシュ同期設定を使ってください。設定項目の正確な定義は [`common/config`](common/config) を参照してください。元プロジェクトの資料にも環境変数の詳細があります。

ログイン後、**Channels** でプロバイダーのキーを登録し、**Tokens** でアクセストークンを作成します。OpenAI 互換クライアントのベース URL を `http://localhost:3000/v1` に設定し、作成したトークンを API キーとして使用します。

```sh
curl http://localhost:3000/v1/models -H 'Authorization: Bearer YOUR_TOKEN'
```

その他の管理エンドポイントは [API ドキュメント](docs/API.md) を参照してください。利用可能なモデルは登録したチャネルによって決まります。

## 開発

- テーマを編集する際は `npm run typecheck --prefix web/default`、`web/air`、または `web/berry` を実行してください。
- 埋め込みフロントエンドを更新する際は、3 テーマそれぞれで `npm run build --prefix web/<テーマ名>` を実行してください。
- バックエンドの変更は `go test ./...` で確認してください。
- [テーマの説明](web/README.md)と [TypeScript 移行の説明](docs/typescript-migration.md)も参照してください。

このフォーク固有の不具合や提案は、[このリポジトリの Issue](https://github.com/infinmalum/one-gateway/issues) に報告してください。報告前に元プロジェクトでも同じ問題が発生するか確認すると、原因を切り分けやすくなります。

## 出典とライセンス

One Gateway は [One API](https://github.com/songquanpeng/one-api) を基にしており、元プロジェクトの著作権は © 2023 JustSong および他の貢献者に帰属します。元の MIT ライセンスと表示は [`LICENSE.upstream`](LICENSE.upstream) に保存しています。このフォーク独自の変更は [Apache License 2.0](LICENSE) で提供します。出典と適用範囲は [`NOTICE`](NOTICE) を参照してください。各ソースファイルや第三者の依存関係に付属するライセンス表示も引き続き適用されます。
