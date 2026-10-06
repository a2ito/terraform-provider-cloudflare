terraform {
  required_providers {
    cloudflare = {
      source = "registry.terraform.io/a2-ito/cloudflare"
    }
  }
}

# api_token は環境変数 CLOUDFLARE_API_TOKEN から読む
provider "cloudflare" {}

variable "zone_id" {
  type = string
}

resource "cloudflare_dns_record" "www" {
  zone_id = var.zone_id
  name    = "www"
  type    = "A"
  content = "192.0.2.1"
  proxied = true
  comment = "managed by terraform"
}

output "record_id" {
  value = cloudflare_dns_record.www.id
}
