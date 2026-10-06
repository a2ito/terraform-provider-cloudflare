resource "cloudflare_dns_record" "www" {
  zone_id = data.cloudflare_zone.this.id
  name    = "www"
  type    = "A"
  content = "192.0.2.1"
  ttl     = 1
  proxied = true
  comment = "managed by terraform"
}
