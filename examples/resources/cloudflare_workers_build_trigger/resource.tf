# Workers Builds がデプロイする Worker。コードは管理せず、Secret だけを持つ。
resource "cloudflare_workers_script" "web" {
  account_id  = "f037e56e89293a057740de681ac9abbe"
  script_name = "web"

  secret_text_bindings = {
    AUTH_SECRET = var.auth_secret
  }
}

resource "cloudflare_workers_build_trigger" "web" {
  account_id           = "f037e56e89293a057740de681ac9abbe"
  script_name          = cloudflare_workers_script.web.script_name
  repo_connection_uuid = "a15901b6-8d2a-422b-9354-11736a567e45"
  build_token_uuid     = "791de442-6f22-43db-b26e-9fecef98af37"

  build_command         = "npm run build"
  deploy_command        = "npx wrangler deploy"
  root_directory        = "apps/web"
  branch_includes       = ["main"]
  path_includes         = ["apps/web/*", "package-lock.json"]
  build_caching_enabled = true

  environment_variables = {
    APP_HOSTNAME = "web.example.com"
  }
}
