terraform {
  required_providers {
    cloudflare = {
      source = "registry.terraform.io/a2ito/cloudflare"
    }
  }
}

# api_token を省略すると環境変数 CLOUDFLARE_API_TOKEN を使う
provider "cloudflare" {}
