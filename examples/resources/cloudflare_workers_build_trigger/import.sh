# <account_id>/<script_name>/<trigger_uuid> の形式で指定する。
# secret_environment_variables の値は取得できないため、import 後の最初の apply で送り直される。
terraform import cloudflare_workers_build_trigger.web f037e56e89293a057740de681ac9abbe/web/7a581eb6-3106-4394-9250-89c92c33284d
