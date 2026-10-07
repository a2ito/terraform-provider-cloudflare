resource "cloudflare_workers_script" "hello" {
  account_id          = "f037e56e89293a057740de681ac9abbe"
  script_name         = "hello"
  content             = file("${path.module}/worker.js")
  compatibility_date  = "2026-01-01"
  compatibility_flags = ["nodejs_compat"]

  plain_text_bindings = {
    MESSAGE = "Hello, world!"
  }
  secret_text_bindings = {
    API_KEY = var.api_key
  }
}
