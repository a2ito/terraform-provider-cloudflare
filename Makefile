BINARY  := terraform-provider-cloudflare
GOBIN   ?= $(shell go env GOPATH)/bin

.PHONY: build install test testacc fmt vet

build:
	go build -o $(BINARY) .

# dev_overrides で参照する $(GOBIN) にインストールする
install:
	go install .

# Cloudflare に接続しないテスト（フェイクサーバを使う）
test:
	go test ./... -count=1

# 実際の Cloudflare にリソースを作るテスト（CLOUDFLARE_API_TOKEN / CLOUDFLARE_ZONE_ID / CLOUDFLARE_ZONE_NAME が必要）
testacc:
	TF_ACC=1 go test ./internal/provider -run '^TestAcc' -count=1 -v -timeout 30m

fmt:
	gofmt -w .

vet:
	go vet ./...
