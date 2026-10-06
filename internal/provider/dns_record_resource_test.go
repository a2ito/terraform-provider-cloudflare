package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/a2-ito/terraform-provider-cloudflare/internal/client"
)

func TestResolveName(t *testing.T) {
	cases := []struct {
		configured, fromAPI, want string
	}{
		{"www", "www.example.com", "www"},
		{"www.example.com", "www.example.com", "www.example.com"},
		{"www", "api.example.com", "api.example.com"},
		{"", "www.example.com", "www.example.com"},
	}
	for _, tc := range cases {
		if got := resolveName(tc.configured, tc.fromAPI); got != tc.want {
			t.Errorf("resolveName(%q, %q) = %q, want %q", tc.configured, tc.fromAPI, got, tc.want)
		}
	}
}

func TestParseImportID(t *testing.T) {
	zoneID, recordID, err := parseImportID("z1/r1")
	if err != nil || zoneID != "z1" || recordID != "r1" {
		t.Errorf("parseImportID(z1/r1) = (%q, %q, %v)", zoneID, recordID, err)
	}
	for _, bad := range []string{"", "z1", "z1/", "/r1", "a/b/c"} {
		if _, _, err := parseImportID(bad); err == nil {
			t.Errorf("parseImportID(%q) succeeded, want error", bad)
		}
	}
}

// fakeCloudflare は DNS レコード API を模したインメモリのサーバ。
type fakeCloudflare struct {
	mu      sync.Mutex
	records map[string]client.DNSRecord
	nextID  int
}

func (f *fakeCloudflare) writeResult(w http.ResponseWriter, status int, result any) {
	w.WriteHeader(status)
	if status >= 400 {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": false,
			"errors":  []client.APIError{{Code: 81044, Message: "Record does not exist."}},
			"result":  nil,
		})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "errors": []any{}, "result": result})
}

// normalize は Cloudflare と同じように短縮名を FQDN にする。
func normalize(rec client.DNSRecord) client.DNSRecord {
	if !strings.HasSuffix(rec.Name, ".example.com") {
		rec.Name += ".example.com"
	}
	return rec
}

func (f *fakeCloudflare) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	// zones/{zone}/dns_records[/{id}]
	if len(parts) < 3 || parts[0] != "zones" || parts[2] != "dns_records" {
		f.writeResult(w, http.StatusNotFound, nil)
		return
	}

	if len(parts) == 3 && r.Method == http.MethodPost {
		var rec client.DNSRecord
		if err := json.NewDecoder(r.Body).Decode(&rec); err != nil {
			f.writeResult(w, http.StatusBadRequest, nil)
			return
		}
		f.nextID++
		rec.ID = fmt.Sprintf("rec%d", f.nextID)
		rec = normalize(rec)
		f.records[rec.ID] = rec
		f.writeResult(w, http.StatusOK, rec)
		return
	}

	if len(parts) != 4 {
		f.writeResult(w, http.StatusNotFound, nil)
		return
	}
	id := parts[3]
	existing, ok := f.records[id]
	if !ok {
		f.writeResult(w, http.StatusNotFound, nil)
		return
	}

	switch r.Method {
	case http.MethodGet:
		f.writeResult(w, http.StatusOK, existing)
	case http.MethodPut:
		var rec client.DNSRecord
		if err := json.NewDecoder(r.Body).Decode(&rec); err != nil {
			f.writeResult(w, http.StatusBadRequest, nil)
			return
		}
		rec.ID = id
		rec = normalize(rec)
		f.records[id] = rec
		f.writeResult(w, http.StatusOK, rec)
	case http.MethodDelete:
		delete(f.records, id)
		f.writeResult(w, http.StatusOK, map[string]string{"id": id})
	default:
		f.writeResult(w, http.StatusMethodNotAllowed, nil)
	}
}

func dnsRecordConfig(content string, ttl int) string {
	return fmt.Sprintf(`
resource "cloudflare_dns_record" "test" {
  zone_id = "zone1"
  name    = "www"
  type    = "A"
  content = %q
  ttl     = %d
  comment = "managed by terraform"
}
`, content, ttl)
}

// TestDNSRecordResource_fake はフェイクサーバを相手に CRUD と import を通しで確認する。
// terraform バイナリが必要だが、Cloudflare には接続しない。
func TestDNSRecordResource_fake(t *testing.T) {
	fake := &fakeCloudflare{records: map[string]client.DNSRecord{}}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	t.Setenv(envAPIToken, "fake-token")
	t.Setenv(envBaseURL, srv.URL)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: dnsRecordConfig("192.0.2.1", 1),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("cloudflare_dns_record.test", "id", "rec1"),
					resource.TestCheckResourceAttr("cloudflare_dns_record.test", "name", "www"),
					resource.TestCheckResourceAttr("cloudflare_dns_record.test", "content", "192.0.2.1"),
					resource.TestCheckResourceAttr("cloudflare_dns_record.test", "proxied", "false"),
				),
			},
			{
				ResourceName:            "cloudflare_dns_record.test",
				ImportState:             true,
				ImportStateIdFunc:       importID,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"name"}, // import 時は FQDN になる
			},
			{
				Config: dnsRecordConfig("192.0.2.2", 300),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("cloudflare_dns_record.test", "id", "rec1"),
					resource.TestCheckResourceAttr("cloudflare_dns_record.test", "content", "192.0.2.2"),
					resource.TestCheckResourceAttr("cloudflare_dns_record.test", "ttl", "300"),
				),
			},
		},
		CheckDestroy: func(_ *terraform.State) error {
			fake.mu.Lock()
			defer fake.mu.Unlock()
			if len(fake.records) != 0 {
				return fmt.Errorf("records still exist after destroy: %v", fake.records)
			}
			return nil
		},
	})
}

func TestDNSRecordResource_invalidImportID(t *testing.T) {
	t.Setenv(envAPIToken, "fake-token")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:        `resource "cloudflare_dns_record" "test" {}`,
				ResourceName:  "cloudflare_dns_record.test",
				ImportState:   true,
				ImportStateId: "no-slash",
				ExpectError:   regexp.MustCompile("<zone_id>/<record_id>"),
			},
		},
	})
}

func importID(s *terraform.State) (string, error) {
	rs, ok := s.RootModule().Resources["cloudflare_dns_record.test"]
	if !ok {
		return "", fmt.Errorf("cloudflare_dns_record.test not found in state")
	}
	return rs.Primary.Attributes["zone_id"] + "/" + rs.Primary.ID, nil
}

// TestAccDNSRecordResource は実際の Cloudflare に対して実行する。
// TF_ACC=1 と CLOUDFLARE_API_TOKEN / CLOUDFLARE_ZONE_ID が必要。
func TestAccDNSRecordResource(t *testing.T) {
	zoneID := os.Getenv("CLOUDFLARE_ZONE_ID")
	if os.Getenv("TF_ACC") != "" && (zoneID == "" || os.Getenv(envAPIToken) == "") {
		t.Fatal("CLOUDFLARE_API_TOKEN と CLOUDFLARE_ZONE_ID を設定してください")
	}

	config := func(content string) string {
		return fmt.Sprintf(`
resource "cloudflare_dns_record" "test" {
  zone_id = %q
  name    = "tf-acc-test"
  type    = "TXT"
  content = %q
  comment = "terraform acceptance test"
}
`, zoneID, content)
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config(`"v1"`),
				Check:  resource.TestCheckResourceAttr("cloudflare_dns_record.test", "content", `"v1"`),
			},
			{
				Config: config(`"v2"`),
				Check:  resource.TestCheckResourceAttr("cloudflare_dns_record.test", "content", `"v2"`),
			},
		},
	})
}
