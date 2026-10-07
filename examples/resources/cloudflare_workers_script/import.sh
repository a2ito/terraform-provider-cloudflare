# <account_id>/<script_name> の形式で指定する。
# secret_text_bindings の値は取得できないため、import 後の最初の apply で再アップロードされる。
terraform import cloudflare_workers_script.hello f037e56e89293a057740de681ac9abbe/hello

# content を管理しない（Workers Builds などがデプロイする）Worker は、末尾に /no-content を付ける。
terraform import cloudflare_workers_script.web f037e56e89293a057740de681ac9abbe/web/no-content
