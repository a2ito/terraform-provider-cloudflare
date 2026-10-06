package provider

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/a2ito/terraform-provider-cloudflare/internal/client"
)

// newFakeZoneServer は GET /zones?name=... に zones を名前で絞って返すサーバを立てる。
func newFakeZoneServer(t *testing.T, zones []client.Zone) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/zones" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":7003,"message":"Could not route"}],"result":null}`))
			return
		}
		name := r.URL.Query().Get("name")
		matched := []client.Zone{}
		for _, z := range zones {
			if z.Name == name {
				matched = append(matched, z)
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "errors": []any{}, "result": matched})
	}))
	t.Cleanup(srv.Close)

	t.Setenv(envAPIToken, "fake-token")
	t.Setenv(envBaseURL, srv.URL)
}

func fakeZone(id, name string) client.Zone {
	z := client.Zone{ID: id, Name: name, Status: "active", NameServers: []string{"ada.ns.cloudflare.com", "bob.ns.cloudflare.com"}}
	z.Account.ID = "acct1"
	return z
}

func TestZoneDataSource_fake(t *testing.T) {
	newFakeZoneServer(t, []client.Zone{fakeZone("zone1", "example.com"), fakeZone("zone2", "example.net")})

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: `data "cloudflare_zone" "test" { name = "example.com" }`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.cloudflare_zone.test", "id", "zone1"),
					resource.TestCheckResourceAttr("data.cloudflare_zone.test", "status", "active"),
					resource.TestCheckResourceAttr("data.cloudflare_zone.test", "paused", "false"),
					resource.TestCheckResourceAttr("data.cloudflare_zone.test", "account_id", "acct1"),
					resource.TestCheckResourceAttr("data.cloudflare_zone.test", "name_servers.#", "2"),
					resource.TestCheckResourceAttr("data.cloudflare_zone.test", "name_servers.0", "ada.ns.cloudflare.com"),
				),
			},
		},
	})
}

func TestZoneDataSource_notFound(t *testing.T) {
	newFakeZoneServer(t, []client.Zone{fakeZone("zone1", "example.com")})

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      `data "cloudflare_zone" "test" { name = "missing.example" }`,
				ExpectError: regexp.MustCompile(`Zone not found`),
			},
		},
	})
}

func TestZoneDataSource_multiple(t *testing.T) {
	newFakeZoneServer(t, []client.Zone{fakeZone("zone1", "example.com"), fakeZone("zone9", "example.com")})

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      `data "cloudflare_zone" "test" { name = "example.com" }`,
				ExpectError: regexp.MustCompile(`Multiple zones found`),
			},
		},
	})
}

// TestAccZoneDataSource は実際の Cloudflare に対して実行する。
// TF_ACC=1 と CLOUDFLARE_API_TOKEN / CLOUDFLARE_ZONE_ID / CLOUDFLARE_ZONE_NAME が必要。
func TestAccZoneDataSource(t *testing.T) {
	zoneID, zoneName := os.Getenv("CLOUDFLARE_ZONE_ID"), os.Getenv("CLOUDFLARE_ZONE_NAME")
	if os.Getenv("TF_ACC") != "" && (zoneID == "" || zoneName == "") {
		t.Fatal("CLOUDFLARE_ZONE_ID と CLOUDFLARE_ZONE_NAME を設定してください")
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: `data "cloudflare_zone" "test" { name = "` + zoneName + `" }`,
				Check:  resource.TestCheckResourceAttr("data.cloudflare_zone.test", "id", zoneID),
			},
		},
	})
}
