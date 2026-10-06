# ユーザーのトークンは <token_id>、アカウントのトークンは <account_id>/<token_id> で指定する。
# トークンの値（value）は import では取得できない。
terraform import cloudflare_api_token.user ed17574386854bf78a67040be0a770b0
terraform import cloudflare_api_token.dns_edit 023e105f4ecef8ad9ca31a8372d0c353/ed17574386854bf78a67040be0a770b0
