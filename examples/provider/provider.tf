terraform {
  required_providers {
    cloudflare = {
      source = "registry.terraform.io/a2ito/cloudflare"
    }
  }
}

# api_token を省略すると環境変数 CLOUDFLARE_API_TOKEN を使う。
# cloudflare_workers_build_trigger を使う場合、api_token がアカウントのトークンなら、
# Workers Builds の API 用にユーザーのトークンを builds_api_token（環境変数 CLOUDFLARE_BUILDS_API_TOKEN）で渡す。
provider "cloudflare" {}
