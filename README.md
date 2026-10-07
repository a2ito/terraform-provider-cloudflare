# terraform-provider-cloudflare

[![ci](https://github.com/a2ito/terraform-provider-cloudflare/actions/workflows/ci.yml/badge.svg)](https://github.com/a2ito/terraform-provider-cloudflare/actions/workflows/ci.yml)

> [!NOTE]
> Cloudflare 公式の provider（[cloudflare/terraform-provider-cloudflare](https://github.com/cloudflare/terraform-provider-cloudflare)）ではない。
> 学習用に書いた非公式の実装で、本番での利用は想定していない。

[terraform-plugin-framework](https://github.com/hashicorp/terraform-plugin-framework) で書いた Cloudflare の Terraform Provider（学習用）。
Cloudflare API v4 は SDK を使わず、`internal/client` の自前クライアントで呼び出す。

## 対応リソース

| 種類 | 名前 | ドキュメント |
| --- | --- | --- |
| Resource | `cloudflare_dns_record` | [docs/resources/dns_record.md](docs/resources/dns_record.md) |
| Resource | `cloudflare_api_token` | [docs/resources/api_token.md](docs/resources/api_token.md) |
| Resource | `cloudflare_workers_script` | [docs/resources/workers_script.md](docs/resources/workers_script.md) |
| Resource | `cloudflare_workers_build_trigger` | [docs/resources/workers_build_trigger.md](docs/resources/workers_build_trigger.md) |
| Data Source | `cloudflare_zone` | [docs/data-sources/zone.md](docs/data-sources/zone.md) |
| Data Source | `cloudflare_api_token_permission_groups` | [docs/data-sources/api_token_permission_groups.md](docs/data-sources/api_token_permission_groups.md) |

Provider の設定は [docs/index.md](docs/index.md) を参照。

## 必要なもの

- Go 1.25.8 以上
- Terraform 1.x

## ローカルで使う

```sh
make install
cp .terraformrc.example ~/.terraformrc   # <GOBIN> を `go env GOPATH`/bin に置き換える

export CLOUDFLARE_API_TOKEN=...
cd examples/complete
terraform plan -var zone_name=example.com
```

`dev_overrides` を使うので `terraform init` は不要。

## 開発

| コマンド | 内容 |
| --- | --- |
| `make build` | バイナリをビルドする |
| `make test` | Cloudflare に接続しないテスト（フェイクサーバを使う。`terraform` が必要） |
| `make testacc` | 実際の Cloudflare に対する acceptance test |
| `make docs` | スキーマと `examples/` から `docs/` を生成する |
| `make vet` / `make fmt` | 静的解析 / 整形 |

### acceptance test

実際に DNS レコード・API Token・Worker・Workers Builds のトリガーを作成・削除する。以下の環境変数が必要。

| 環境変数 | 内容 |
| --- | --- |
| `CLOUDFLARE_API_TOKEN` | `Zone:Read`・`DNS:Edit`・`Account API Tokens:Edit`・`Workers Scripts:Edit` 権限を持つ API Token |
| `CLOUDFLARE_ZONE_ID` | テストに使う Zone の ID |
| `CLOUDFLARE_ZONE_NAME` | テストに使う Zone の名前 |
| `CLOUDFLARE_ACCOUNT_ID` | `cloudflare_api_token`・`cloudflare_workers_script`・`cloudflare_workers_build_trigger` のテストに使うアカウントの ID |
| `CLOUDFLARE_BUILDS_API_TOKEN` | `Workers CI Write`（UI では Workers Builds Configuration : Edit）を持つ**ユーザーの** API Token |
| `CLOUDFLARE_REPO_CONNECTION_UUID` | トリガーのテストに使う既存のリポジトリの接続の UUID |
| `CLOUDFLARE_BUILD_TOKEN_UUID` | トリガーのテストに使う既存のビルドトークンの UUID |

トリガーのテストは、push されないブランチ（`tf-acc-test-never-pushed`）だけをビルドするトリガーを作るので、ビルドは走らない。

### API Token を管理するときの注意

- `cloudflare_api_token` の値（`value`）は作成時にしか取得できないため、state に平文で保存される。state は暗号化した remote backend で管理する
- provider が使うトークン自身にトークン作成の権限が要る（ユーザーのトークンは `API Tokens:Edit`、アカウントのトークンは `Account API Tokens:Edit`）。この最初の 1 本はダッシュボードで作る
- provider が使っているトークン自身を Terraform で管理して destroy すると、それ以降の API 呼び出しができなくなる

### Workers スクリプトを管理するときの注意

- 対応しているのは ES Modules 形式の単一ファイルのスクリプトと、`plain_text`・`secret_text` のバインディングのみ。apply するとバインディングは Terraform の設定で丸ごと置き換わる（ダッシュボードで追加した KV などのバインディングは消える）
- `secret_text_bindings` の値は state に平文で保存される。また Cloudflare は値を返さないため、Terraform の外での値の変更は検出できない
- `content` を省略すると、コードを管理しないモードになる。Workers Builds や wrangler がデプロイする Worker の Secret だけを Terraform で持つときに使う。Secret は 1 件ずつ反映し、コードと他のバインディング（D1・assets など）には触らない
  - このモードでは `plain_text_bindings`・`main_module`・`compatibility_*` は指定できない。`wrangler deploy` が上書きするため、wrangler の設定で持つ
  - 既存の Worker は `<account_id>/<script_name>/no-content` で import する（`/no-content` を付けないと本体まで読み、plan に本体の差分が出る）
  - destroy すると Worker ごと消える。デプロイ先の Worker を守るなら `lifecycle { prevent_destroy = true }` を付ける

### Workers Builds を管理するときの注意

- **Builds の API はアカウントのトークンを受け付けない**（`12006: Invalid token` が返る。権限不足の `10000` とは別）。`api_token` がアカウントのトークンなら、ユーザーのトークンを `builds_api_token` に渡す
- 権限の名前は UI と API で違う。UI の「Workers Builds Configuration」は、API（権限グループの一覧）では `Workers CI Write` / `Workers CI Read`
- リポジトリの接続（GitHub App のインストール）とビルドトークンはダッシュボードで作り、UUID を指定する
- 作成時は `trigger_name` が必須（無いと `12002: Invalid request body`）。省略したときは、ダッシュボードと同じく Worker の tag を名前にする

### ドキュメント

`docs/` は生成物なので直接編集しない。
説明文はスキーマの `Description` に、使用例は `examples/` の以下のファイルに書いて `make docs` を実行する。

- `examples/provider/provider.tf`
- `examples/resources/<リソース名>/resource.tf`、`import.sh`
- `examples/data-sources/<データソース名>/data-source.tf`

## リリース

`v*` のタグを push すると `.github/workflows/release.yml` が GoReleaser で GitHub Release を作り、Terraform Registry が取り込む。

```sh
git switch main && git pull
git tag v0.1.0
git push origin v0.1.0
```

- Registry が検証するため、`SHA256SUMS` は GPG で署名する。鍵はリポジトリの Secrets（`GPG_PRIVATE_KEY` / `PASSPHRASE`）に置き、公開鍵は Registry の Signing Keys に登録する
- 手元で成果物の形だけ確かめるなら `go run github.com/goreleaser/goreleaser/v2@latest release --snapshot --clean --skip=sign,publish`

## ライセンス

[MPL-2.0](LICENSE)
