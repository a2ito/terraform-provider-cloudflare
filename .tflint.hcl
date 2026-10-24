# このリポジトリの .tf は examples/ だけで、tfplugindocs が docs に埋め込む断片。
# 版の指定や、宣言だけして使わない data は断片として正しいので、その指摘は止める
rule "terraform_required_version" {
  enabled = false
}
rule "terraform_required_providers" {
  enabled = false
}
rule "terraform_unused_declarations" {
  enabled = false
}
