package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/a2ito/terraform-provider-cloudflare/internal/client"
)

func TestParseWorkersScriptImportID(t *testing.T) {
	accountID, name, err := parseWorkersScriptImportID("acct1/hello")
	if err != nil || accountID != "acct1" || name != "hello" {
		t.Errorf("parseWorkersScriptImportID(acct1/hello) = (%q, %q, %v)", accountID, name, err)
	}
	for _, bad := range []string{"", "acct1", "acct1/", "/hello", "a/b/c"} {
		if _, _, err := parseWorkersScriptImportID(bad); err == nil {
			t.Errorf("parseWorkersScriptImportID(%q) succeeded, want error", bad)
		}
	}
}

func TestApplySettingsKeepsSecretValues(t *testing.T) {
	ctx := context.Background()
	prior := workersScriptModel{
		CompatibilityFlags: types.SetNull(types.StringType),
		PlainTextBindings:  types.MapNull(types.StringType),
		SecretTextBindings: types.MapValueMust(types.StringType, map[string]attr.Value{"TOKEN": types.StringValue("s1")}),
	}
	settings := client.WorkerScriptSettings{
		CompatibilityDate: "2026-01-01",
		Bindings: []client.WorkerBinding{
			{Type: client.WorkerBindingSecretText, Name: "TOKEN"},
			{Type: client.WorkerBindingSecretText, Name: "ADDED_OUTSIDE"},
			{Type: "kv_namespace", Name: "IGNORED"},
		},
	}

	got, diags := applySettings(ctx, prior, settings)
	if diags.HasError() {
		t.Fatalf("applySettings: %v", diags)
	}
	secret, _ := stringMap(ctx, got.SecretTextBindings)
	if secret["TOKEN"] != "s1" || secret["ADDED_OUTSIDE"] != "" || len(secret) != 2 {
		t.Errorf("unexpected secret bindings: %v", secret)
	}
	if !got.PlainTextBindings.IsNull() || !got.CompatibilityFlags.IsNull() {
		t.Errorf("empty values should stay null: plain=%v flags=%v", got.PlainTextBindings, got.CompatibilityFlags)
	}
	if got.CompatibilityDate.ValueString() != "2026-01-01" {
		t.Errorf("compatibility_date = %v", got.CompatibilityDate)
	}
}

// fakeWorkerScript はフェイクサーバが保持するスクリプト 1 件。
type fakeWorkerScript struct {
	meta    client.WorkerScriptMetadata
	content string
}

// fakeWorkers は Workers スクリプト API を模したインメモリのサーバ。
type fakeWorkers struct {
	mu      sync.Mutex
	scripts map[string]fakeWorkerScript // key: "<account_id>/<script_name>"
}

const fakeDefaultCompatibilityDate = "2025-01-01"

func (f *fakeWorkers) writeResult(w http.ResponseWriter, status int, result any) {
	w.WriteHeader(status)
	if status >= 400 {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": false,
			"errors":  []client.APIError{{Code: 10007, Message: "workers.api.error.script_not_found"}},
			"result":  nil,
		})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "errors": []any{}, "result": result})
}

func (f *fakeWorkers) upload(r *http.Request) (fakeWorkerScript, error) {
	mr, err := r.MultipartReader()
	if err != nil {
		return fakeWorkerScript{}, err
	}
	parts := map[string]string{}
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fakeWorkerScript{}, err
		}
		b, err := io.ReadAll(p)
		if err != nil {
			return fakeWorkerScript{}, err
		}
		parts[p.FormName()] = string(b)
	}

	var s fakeWorkerScript
	if err := json.Unmarshal([]byte(parts["metadata"]), &s.meta); err != nil {
		return fakeWorkerScript{}, err
	}
	content, ok := parts[s.meta.MainModule]
	if !ok {
		return fakeWorkerScript{}, fmt.Errorf("main module %q not uploaded", s.meta.MainModule)
	}
	s.content = content
	if s.meta.CompatibilityDate == "" {
		s.meta.CompatibilityDate = fakeDefaultCompatibilityDate
	}
	return s, nil
}

func (f *fakeWorkers) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	// accounts/{account}/workers/scripts/{name}[/settings | /content/v2]
	if len(parts) < 5 || parts[0] != "accounts" || parts[2] != "workers" || parts[3] != "scripts" {
		f.writeResult(w, http.StatusNotFound, nil)
		return
	}
	key := parts[1] + "/" + parts[4]
	sub := strings.Join(parts[5:], "/")

	if sub == "" && r.Method == http.MethodPut {
		s, err := f.upload(r)
		if err != nil {
			f.writeResult(w, http.StatusBadRequest, nil)
			return
		}
		f.scripts[key] = s
		f.writeResult(w, http.StatusOK, client.WorkerScript{ID: parts[4], ETag: "etag"})
		return
	}

	existing, ok := f.scripts[key]
	if !ok {
		f.writeResult(w, http.StatusNotFound, nil)
		return
	}

	switch {
	case sub == "settings" && r.Method == http.MethodGet:
		bindings := make([]client.WorkerBinding, 0, len(existing.meta.Bindings))
		// 実 API と同様に順序は保証せず、secret_text の値は返さない
		for i := len(existing.meta.Bindings) - 1; i >= 0; i-- {
			b := existing.meta.Bindings[i]
			if b.Type == client.WorkerBindingSecretText {
				b.Text = ""
			}
			bindings = append(bindings, b)
		}
		f.writeResult(w, http.StatusOK, client.WorkerScriptSettings{
			CompatibilityDate:  existing.meta.CompatibilityDate,
			CompatibilityFlags: existing.meta.CompatibilityFlags,
			Bindings:           bindings,
		})
	case sub == "content/v2" && r.Method == http.MethodGet:
		_, _ = w.Write([]byte(existing.content))
	case sub == "" && r.Method == http.MethodDelete:
		delete(f.scripts, key)
		f.writeResult(w, http.StatusOK, nil)
	default:
		f.writeResult(w, http.StatusMethodNotAllowed, nil)
	}
}

const workersScriptConfigV1 = `
resource "cloudflare_workers_script" "test" {
  account_id  = "acct1"
  script_name = "hello"
  content     = "export default { fetch() { return new Response('v1') } }"

  plain_text_bindings = {
    MESSAGE = "hello"
    LEVEL   = "debug"
  }
  secret_text_bindings = {
    TOKEN = "s1"
  }
}
`

const workersScriptConfigV2 = `
resource "cloudflare_workers_script" "test" {
  account_id          = "acct1"
  script_name         = "hello"
  main_module         = "index.mjs"
  content             = "export default { fetch() { return new Response('v2') } }"
  compatibility_date  = "2026-01-01"
  compatibility_flags = ["nodejs_compat"]

  plain_text_bindings = {
    MESSAGE = "bye"
  }
}
`

// TestWorkersScriptResource_fake はフェイクサーバを相手に CRUD・import・ドリフト検出を通しで確認する。
func TestWorkersScriptResource_fake(t *testing.T) {
	fake := &fakeWorkers{scripts: map[string]fakeWorkerScript{}}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	t.Setenv(envAPIToken, "fake-token")
	t.Setenv(envBaseURL, srv.URL)

	const addr = "cloudflare_workers_script.test"
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: workersScriptConfigV1,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "id", "hello"),
					resource.TestCheckResourceAttr(addr, "main_module", "worker.js"),
					resource.TestCheckResourceAttr(addr, "compatibility_date", fakeDefaultCompatibilityDate),
					resource.TestCheckResourceAttr(addr, "plain_text_bindings.MESSAGE", "hello"),
					resource.TestCheckResourceAttr(addr, "secret_text_bindings.TOKEN", "s1"),
				),
			},
			{
				ResourceName:      addr,
				ImportState:       true,
				ImportStateId:     "acct1/hello",
				ImportStateVerify: true,
				// secret_text の値は API から取得できない
				ImportStateVerifyIgnore: []string{"secret_text_bindings"},
			},
			{
				Config: workersScriptConfigV2,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "compatibility_date", "2026-01-01"),
					resource.TestCheckResourceAttr(addr, "compatibility_flags.#", "1"),
					resource.TestCheckNoResourceAttr(addr, "secret_text_bindings"),
					func(_ *terraform.State) error {
						fake.mu.Lock()
						defer fake.mu.Unlock()
						s := fake.scripts["acct1/hello"]
						if s.meta.MainModule != "index.mjs" || !strings.Contains(s.content, "v2") || len(s.meta.Bindings) != 1 {
							return fmt.Errorf("unexpected uploaded script: %+v", s)
						}
						return nil
					},
				),
			},
			{
				// Terraform の外でスクリプトが書き換えられたら差分として検出する
				PreConfig: func() {
					fake.mu.Lock()
					defer fake.mu.Unlock()
					s := fake.scripts["acct1/hello"]
					s.content = "export default {}"
					fake.scripts["acct1/hello"] = s
				},
				Config:             workersScriptConfigV2,
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
		},
		CheckDestroy: func(_ *terraform.State) error {
			fake.mu.Lock()
			defer fake.mu.Unlock()
			if len(fake.scripts) != 0 {
				return fmt.Errorf("scripts still exist after destroy: %v", fake.scripts)
			}
			return nil
		},
	})
}

func TestWorkersScriptResource_duplicateBindingName(t *testing.T) {
	fake := &fakeWorkers{scripts: map[string]fakeWorkerScript{}}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	t.Setenv(envAPIToken, "fake-token")
	t.Setenv(envBaseURL, srv.URL)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: `
resource "cloudflare_workers_script" "test" {
  account_id           = "acct1"
  script_name          = "hello"
  content              = "export default {}"
  plain_text_bindings  = { NAME = "a" }
  secret_text_bindings = { NAME = "b" }
}
`,
				ExpectError: regexp.MustCompile("Duplicate binding name"),
			},
		},
	})
}

func TestWorkersScriptResource_invalidImportID(t *testing.T) {
	t.Setenv(envAPIToken, "fake-token")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:        `resource "cloudflare_workers_script" "test" {}`,
				ResourceName:  "cloudflare_workers_script.test",
				ImportState:   true,
				ImportStateId: "no-slash",
				ExpectError:   regexp.MustCompile("<account_id>/<script_name>"),
			},
		},
	})
}

// TestAccWorkersScriptResource は実際の Cloudflare に対して実行する。
// TF_ACC=1 と CLOUDFLARE_API_TOKEN / CLOUDFLARE_ACCOUNT_ID が必要。
func TestAccWorkersScriptResource(t *testing.T) {
	accountID := os.Getenv("CLOUDFLARE_ACCOUNT_ID")
	if os.Getenv("TF_ACC") != "" && (accountID == "" || os.Getenv(envAPIToken) == "") {
		t.Fatal("CLOUDFLARE_API_TOKEN と CLOUDFLARE_ACCOUNT_ID を設定してください")
	}

	config := func(message string) string {
		return fmt.Sprintf(`
resource "cloudflare_workers_script" "test" {
  account_id         = %q
  script_name        = "tf-acc-test"
  compatibility_date = "2026-01-01"
  content            = <<-EOT
    export default {
      async fetch(request, env) {
        return new Response(env.MESSAGE)
      }
    }
  EOT

  plain_text_bindings  = { MESSAGE = %q }
  secret_text_bindings = { TOKEN = "terraform-acceptance-test" }
}
`, accountID, message)
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config("v1"),
				Check:  resource.TestCheckResourceAttr("cloudflare_workers_script.test", "plain_text_bindings.MESSAGE", "v1"),
			},
			{
				Config: config("v2"),
				Check:  resource.TestCheckResourceAttr("cloudflare_workers_script.test", "plain_text_bindings.MESSAGE", "v2"),
			},
		},
	})
}
