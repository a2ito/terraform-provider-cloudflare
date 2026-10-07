# <account_id>/<script_name> の形式で指定する。
# secret_text_bindings の値は取得できないため、import 後の最初の apply で再アップロードされる。
terraform import cloudflare_workers_script.hello f037e56e89293a057740de681ac9abbe/hello
