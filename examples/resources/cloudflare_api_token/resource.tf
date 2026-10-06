data "cloudflare_api_token_permission_groups" "all" {
  account_id = var.account_id
}

# 1 つの Zone の DNS だけを編集できるアカウントのトークン
resource "cloudflare_api_token" "dns_edit" {
  account_id = var.account_id
  name       = "dns-edit-example-com"
  expires_on = "2027-01-01T00:00:00Z"

  policies = [{
    effect = "allow"
    permission_groups = [
      data.cloudflare_api_token_permission_groups.all.ids["DNS Write"],
      data.cloudflare_api_token_permission_groups.all.ids["Zone Read"],
    ]
    resources = jsonencode({
      "com.cloudflare.api.account.zone.${data.cloudflare_zone.this.id}" = "*"
    })
  }]

  condition = {
    request_ip = {
      in = ["192.0.2.0/24"]
    }
  }
}

# 値は sensitive。state には平文で保存されるので、state は暗号化して保管する
output "dns_edit_token" {
  value     = cloudflare_api_token.dns_edit.value
  sensitive = true
}
