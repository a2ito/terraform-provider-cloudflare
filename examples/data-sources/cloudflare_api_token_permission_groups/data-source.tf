# account_id を省略するとユーザーのトークン用の一覧になる
data "cloudflare_api_token_permission_groups" "all" {
  account_id = "023e105f4ecef8ad9ca31a8372d0c353"
}

output "dns_write_id" {
  value = data.cloudflare_api_token_permission_groups.all.ids["DNS Write"]
}
