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
	"time"

	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/a2ito/terraform-provider-cloudflare/internal/client"
)

func TestPreserveJSON(t *testing.T) {
	prior := types.StringValue(`{"b":"*","a":{"x":"*"}}`)
	if got := preserveJSON(prior, json.RawMessage(`{ "a": {"x": "*"}, "b": "*" }`)); got != prior {
		t.Errorf("semantically equal JSON should keep prior, got %s", got)
	}
	if got := preserveJSON(prior, json.RawMessage(`{"a":"*"}`)); got.ValueString() != `{"a":"*"}` {
		t.Errorf("different JSON should use API value, got %s", got)
	}
	if got := preserveJSON(types.StringNull(), json.RawMessage(`{"a":"*"}`)); got.ValueString() != `{"a":"*"}` {
		t.Errorf("null prior should use API value, got %s", got)
	}
}

func TestPreserveTime(t *testing.T) {
	prior := types.StringValue("2027-01-01T09:00:00+09:00")
	if got := preserveTime(prior, "2027-01-01T00:00:00Z"); got != prior {
		t.Errorf("same instant should keep prior, got %s", got)
	}
	if got := preserveTime(prior, "2027-01-02T00:00:00Z"); got.ValueString() != "2027-01-02T00:00:00Z" {
		t.Errorf("different instant should use API value, got %s", got)
	}
	if got := preserveTime(prior, ""); !got.IsNull() {
		t.Errorf("empty API value should be null, got %s", got)
	}
}

func TestParseAPITokenImportID(t *testing.T) {
	cases := []struct {
		id, account, token string
	}{
		{"t1", "", "t1"},
		{"a1/t1", "a1", "t1"},
	}
	for _, tc := range cases {
		a, tok, err := parseAPITokenImportID(tc.id)
		if err != nil || a != tc.account || tok != tc.token {
			t.Errorf("parseAPITokenImportID(%q) = (%q, %q, %v)", tc.id, a, tok, err)
		}
	}
	for _, bad := range []string{"", "/t1", "a1/", "a/b/c"} {
		if _, _, err := parseAPITokenImportID(bad); err == nil {
			t.Errorf("parseAPITokenImportID(%q) succeeded, want error", bad)
		}
	}
}

// fakeTokenAPI は API Token の API を模したインメモリのサーバ。
// 本物と同じく、resources の JSON は整形し直し、日時は UTC に直して返す。
type fakeTokenAPI struct {
	mu     sync.Mutex
	tokens map[string]client.APIToken // key: "<owner>/<id>"。owner は "user" か account ID
	groups []client.PermissionGroup
	nextID int
}

func newFakeTokenAPI(t *testing.T) *fakeTokenAPI {
	t.Helper()
	f := &fakeTokenAPI{
		tokens: map[string]client.APIToken{},
		groups: []client.PermissionGroup{
			{ID: "g-dns-write", Name: "DNS Write", Scopes: []string{"com.cloudflare.api.account.zone"}},
			{ID: "g-zone-read", Name: "Zone Read", Scopes: []string{"com.cloudflare.api.account.zone"}},
			{ID: "g-dup-1", Name: "Duplicated", Scopes: []string{"com.cloudflare.api.account"}},
			{ID: "g-dup-2", Name: "Duplicated", Scopes: []string{"com.cloudflare.api.user"}},
		},
	}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	t.Setenv(envAPIToken, "fake-token")
	t.Setenv(envBaseURL, srv.URL)
	return f
}

func writeEnvelope(w http.ResponseWriter, status int, result any) {
	w.WriteHeader(status)
	if status >= 400 {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": false, "errors": []client.APIError{{Code: 1000, Message: http.StatusText(status)}}, "result": nil,
		})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "errors": []any{}, "result": result})
}

func normalizeToken(tok client.APIToken) (client.APIToken, error) {
	for i, p := range tok.Policies {
		var v any
		if err := json.Unmarshal(p.Resources, &v); err != nil {
			return tok, err
		}
		b, _ := json.MarshalIndent(v, "", "  ") // わざと整形を変える
		tok.Policies[i].Resources = b
		tok.Policies[i].ID = fmt.Sprintf("policy%d", i)
	}
	for _, ts := range []*string{&tok.ExpiresOn, &tok.NotBefore} {
		if *ts == "" {
			continue
		}
		parsed, err := time.Parse(time.RFC3339, *ts)
		if err != nil {
			return tok, err
		}
		*ts = parsed.UTC().Format(time.RFC3339)
	}
	return tok, nil
}

func (f *fakeTokenAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	// /user/tokens[/...] または /accounts/{id}/tokens[/...]
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	var owner string
	var rest []string
	switch {
	case len(parts) >= 2 && parts[0] == "user" && parts[1] == "tokens":
		owner, rest = "user", parts[2:]
	case len(parts) >= 3 && parts[0] == "accounts" && parts[2] == "tokens":
		owner, rest = parts[1], parts[3:]
	default:
		writeEnvelope(w, http.StatusNotFound, nil)
		return
	}

	if len(rest) == 1 && rest[0] == "permission_groups" && r.Method == http.MethodGet {
		writeEnvelope(w, http.StatusOK, f.groups)
		return
	}

	decode := func() (client.APIToken, bool) {
		var tok client.APIToken
		if err := json.NewDecoder(r.Body).Decode(&tok); err != nil {
			writeEnvelope(w, http.StatusBadRequest, nil)
			return tok, false
		}
		tok, err := normalizeToken(tok)
		if err != nil {
			writeEnvelope(w, http.StatusBadRequest, nil)
			return tok, false
		}
		return tok, true
	}

	if len(rest) == 0 && r.Method == http.MethodPost {
		tok, ok := decode()
		if !ok {
			return
		}
		f.nextID++
		tok.ID = fmt.Sprintf("tok%d", f.nextID)
		tok.Status = "active" // 作成時の status は無視される
		tok.IssuedOn = "2026-10-06T00:00:00Z"
		tok.ModifiedOn = tok.IssuedOn
		f.tokens[owner+"/"+tok.ID] = tok
		created := tok
		created.Value = "secret-" + tok.ID
		writeEnvelope(w, http.StatusOK, created)
		return
	}

	if len(rest) != 1 {
		writeEnvelope(w, http.StatusNotFound, nil)
		return
	}
	key := owner + "/" + rest[0]
	existing, ok := f.tokens[key]
	if !ok {
		writeEnvelope(w, http.StatusNotFound, nil)
		return
	}

	switch r.Method {
	case http.MethodGet:
		writeEnvelope(w, http.StatusOK, existing)
	case http.MethodPut:
		tok, ok := decode()
		if !ok {
			return
		}
		tok.ID, tok.IssuedOn, tok.ModifiedOn = existing.ID, existing.IssuedOn, "2026-10-07T00:00:00Z"
		if tok.Status == "" {
			tok.Status = existing.Status
		}
		f.tokens[key] = tok
		writeEnvelope(w, http.StatusOK, tok)
	case http.MethodDelete:
		delete(f.tokens, key)
		writeEnvelope(w, http.StatusOK, map[string]string{"id": existing.ID})
	default:
		writeEnvelope(w, http.StatusMethodNotAllowed, nil)
	}
}

func (f *fakeTokenAPI) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.tokens)
}

const accountTokenConfigV1 = `
data "cloudflare_api_token_permission_groups" "all" {
  account_id = "acct1"
}

resource "cloudflare_api_token" "test" {
  account_id = "acct1"
  name       = "ci"
  status     = "disabled"
  expires_on = "2027-01-01T09:00:00+09:00"

  policies = [{
    effect            = "allow"
    permission_groups = [data.cloudflare_api_token_permission_groups.all.ids["DNS Write"]]
    resources = jsonencode({
      "com.cloudflare.api.account.acct1" = {
        "com.cloudflare.api.account.zone.*" = "*"
      }
    })
  }]

  condition = {
    request_ip = {
      in = ["192.0.2.0/24"]
    }
  }
}
`

const accountTokenConfigV2 = `
resource "cloudflare_api_token" "test" {
  account_id = "acct1"
  name       = "ci-renamed"
  status     = "active"

  policies = [{
    effect            = "allow"
    permission_groups = ["g-dns-write", "g-zone-read"]
    resources         = jsonencode({ "com.cloudflare.api.account.zone.zone1" = "*" })
  }]
}
`

// TestAPITokenResource_account はアカウントのトークンの作成・import・更新・削除を通しで確認する。
func TestAPITokenResource_account(t *testing.T) {
	fake := newFakeTokenAPI(t)
	const addr = "cloudflare_api_token.test"

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: accountTokenConfigV1,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "id", "tok1"),
					resource.TestCheckResourceAttr(addr, "value", "secret-tok1"),
					resource.TestCheckResourceAttr(addr, "status", "disabled"),
					resource.TestCheckResourceAttr(addr, "expires_on", "2027-01-01T09:00:00+09:00"),
					resource.TestCheckResourceAttr(addr, "policies.0.permission_groups.#", "1"),
					resource.TestCheckTypeSetElemAttr(addr, "policies.0.permission_groups.*", "g-dns-write"),
					resource.TestCheckResourceAttr(addr, "condition.request_ip.in.0", "192.0.2.0/24"),
					resource.TestCheckResourceAttr(addr, "issued_on", "2026-10-06T00:00:00Z"),
				),
			},
			{
				ResourceName: addr,
				ImportState:  true,
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					return "acct1/" + s.RootModule().Resources[addr].Primary.ID, nil
				},
				ImportStateVerify: true,
				// value は作成時にしか取れない。expires_on は API が UTC で返すため表記が変わる。
				ImportStateVerifyIgnore: []string{"value", "expires_on"},
			},
			{
				Config: accountTokenConfigV2,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "id", "tok1"),
					resource.TestCheckResourceAttr(addr, "name", "ci-renamed"),
					resource.TestCheckResourceAttr(addr, "status", "active"),
					resource.TestCheckResourceAttr(addr, "value", "secret-tok1"), // 更新しても値は変わらない
					resource.TestCheckNoResourceAttr(addr, "expires_on"),
					resource.TestCheckNoResourceAttr(addr, "condition"),
					resource.TestCheckResourceAttr(addr, "policies.0.permission_groups.#", "2"),
					resource.TestCheckResourceAttr(addr, "modified_on", "2026-10-07T00:00:00Z"),
				),
			},
		},
		CheckDestroy: func(_ *terraform.State) error {
			if n := fake.count(); n != 0 {
				return fmt.Errorf("%d tokens still exist after destroy", n)
			}
			return nil
		},
	})
}

// TestAPITokenResource_user は account_id を省略したときにユーザーのトークンになることを確認する。
func TestAPITokenResource_user(t *testing.T) {
	newFakeTokenAPI(t)
	const addr = "cloudflare_api_token.test"

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: `
resource "cloudflare_api_token" "test" {
  name = "user-token"
  policies = [{
    effect            = "allow"
    permission_groups = ["g-zone-read"]
    resources         = jsonencode({ "com.cloudflare.api.account.zone.*" = "*" })
  }]
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr(addr, "account_id"),
					resource.TestCheckResourceAttr(addr, "status", "active"),
					resource.TestCheckResourceAttr(addr, "value", "secret-tok1"),
				),
			},
			{
				ResourceName:            addr,
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"value"},
			},
		},
	})
}

func TestAPITokenResource_invalidResourcesJSON(t *testing.T) {
	newFakeTokenAPI(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: `
resource "cloudflare_api_token" "test" {
  name = "bad"
  policies = [{
    effect            = "allow"
    permission_groups = ["g-zone-read"]
    resources         = "not json"
  }]
}
`,
				ExpectError: regexp.MustCompile(`Invalid resources JSON`),
			},
		},
	})
}

func TestIDsByName(t *testing.T) {
	ids, dup := idsByName([]client.PermissionGroup{
		{ID: "1", Name: "DNS Write"},
		{ID: "2", Name: "Dup"},
		{ID: "3", Name: "Dup"},
	})
	if len(ids) != 1 || ids["DNS Write"] != "1" {
		t.Errorf("ids = %v", ids)
	}
	if len(dup) != 1 || dup[0] != "Dup" {
		t.Errorf("duplicated = %v", dup)
	}
}

func TestPermissionGroupsDataSource_fake(t *testing.T) {
	newFakeTokenAPI(t)
	const addr = "data.cloudflare_api_token_permission_groups.test"

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: `data "cloudflare_api_token_permission_groups" "test" {}`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "permission_groups.#", "4"),
					resource.TestCheckResourceAttr(addr, "ids.%", "2"),
					resource.TestCheckResourceAttr(addr, "ids.DNS Write", "g-dns-write"),
					resource.TestCheckNoResourceAttr(addr, "ids.Duplicated"),
					resource.TestCheckResourceAttr(addr, "permission_groups.0.scopes.0", "com.cloudflare.api.account.zone"),
				),
			},
		},
	})
}

// TestAccAPITokenResource は実際の Cloudflare にトークンを作って消す。
// TF_ACC=1 と CLOUDFLARE_API_TOKEN（Account API Tokens Write を含む）/ CLOUDFLARE_ACCOUNT_ID / CLOUDFLARE_ZONE_ID が必要。
func TestAccAPITokenResource(t *testing.T) {
	accountID, zoneID := os.Getenv("CLOUDFLARE_ACCOUNT_ID"), os.Getenv("CLOUDFLARE_ZONE_ID")
	if os.Getenv("TF_ACC") != "" && (accountID == "" || zoneID == "") {
		t.Fatal("CLOUDFLARE_ACCOUNT_ID と CLOUDFLARE_ZONE_ID を設定してください")
	}

	config := func(name string) string {
		return fmt.Sprintf(`
data "cloudflare_api_token_permission_groups" "all" {
  account_id = %[1]q
}

resource "cloudflare_api_token" "test" {
  account_id = %[1]q
  name       = %[3]q
  expires_on = "2030-01-01T00:00:00Z"

  policies = [{
    effect            = "allow"
    permission_groups = [data.cloudflare_api_token_permission_groups.all.ids["DNS Read"]]
    resources         = jsonencode({ "com.cloudflare.api.account.zone.%[2]s" = "*" })
  }]
}
`, accountID, zoneID, name)
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config("tf-acc-test"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("cloudflare_api_token.test", "value"),
					resource.TestCheckResourceAttr("cloudflare_api_token.test", "status", "active"),
				),
			},
			{
				Config: config("tf-acc-test-renamed"),
				Check:  resource.TestCheckResourceAttr("cloudflare_api_token.test", "name", "tf-acc-test-renamed"),
			},
		},
	})
}
