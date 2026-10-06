# terraform-provider-cloudflare

[terraform-plugin-framework](https://github.com/hashicorp/terraform-plugin-framework) で書いた Cloudflare の Terraform Provider（学習用）。
Cloudflare API v4 は SDK を使わず、`internal/client` の自前クライアントで呼び出す。

## 対応リソース

| 種類 | 名前 | ドキュメント |
| --- | --- | --- |
| Resource | `cloudflare_dns_record` | [docs/resources/dns_record.md](docs/resources/dns_record.md) |
| Data Source | `cloudflare_zone` | [docs/data-sources/zone.md](docs/data-sources/zone.md) |

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

実際にレコードを作成・削除する。以下の環境変数が必要。

| 環境変数 | 内容 |
| --- | --- |
| `CLOUDFLARE_API_TOKEN` | `Zone:Read` と `DNS:Edit` 権限を持つ API Token |
| `CLOUDFLARE_ZONE_ID` | テストに使う Zone の ID |
| `CLOUDFLARE_ZONE_NAME` | テストに使う Zone の名前 |

### ドキュメント

`docs/` は生成物なので直接編集しない。
説明文はスキーマの `Description` に、使用例は `examples/` の以下のファイルに書いて `make docs` を実行する。

- `examples/provider/provider.tf`
- `examples/resources/<リソース名>/resource.tf`、`import.sh`
- `examples/data-sources/<データソース名>/data-source.tf`
