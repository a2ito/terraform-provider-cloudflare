package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
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
	cases := []struct {
		id          string
		withContent bool
	}{
		{"acct1/hello", true},
		{"acct1/hello/no-content", false},
	}
	for _, tc := range cases {
		accountID, name, withContent, err := parseWorkersScriptImportID(tc.id)
		if err != nil || accountID != "acct1" || name != "hello" || withContent != tc.withContent {
			t.Errorf("parseWorkersScriptImportID(%q) = (%q, %q, %v, %v)", tc.id, accountID, name, withContent, err)
		}
	}
	for _, bad := range []string{"", "acct1", "acct1/", "/hello", "a/b/c", "acct1/hello/no-content/x"} {
		if _, _, _, err := parseWorkersScriptImportID(bad); err == nil {
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
	mu         sync.Mutex
	scripts    map[string]fakeWorkerScript // key: "<account_id>/<script_name>"
	secretPuts []string                    // Secret の API で更新された名前（順に）

	// Workers Builds
	triggers map[string]client.BuildTrigger                        // key: trigger UUID
	buildEnv map[string]map[string]client.BuildEnvironmentVariable // key: trigger UUID
	nextID   int
}

func newFakeWorkers() *fakeWorkers {
	return &fakeWorkers{
		scripts:  map[string]fakeWorkerScript{},
		triggers: map[string]client.BuildTrigger{},
		buildEnv: map[string]map[string]client.BuildEnvironmentVariable{},
	}
}

// fakeTag は Worker の tag。実 API では名前と無関係な UUID だが、テストでは名前から作る。
func fakeTag(name string) string { return "tag-" + name }

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
	if len(parts) >= 3 && parts[0] == "accounts" && parts[2] == "builds" {
		f.serveBuilds(w, r, parts[3:])
		return
	}
	// accounts/{account}/workers/scripts
	if len(parts) == 4 && parts[0] == "accounts" && parts[2] == "workers" && parts[3] == "scripts" && r.Method == http.MethodGet {
		list := []client.WorkerScriptSummary{}
		for key := range f.scripts {
			if account, name, _ := strings.Cut(key, "/"); account == parts[1] {
				list = append(list, client.WorkerScriptSummary{ID: name, Tag: fakeTag(name)})
			}
		}
		f.writeResult(w, http.StatusOK, list)
		return
	}
	// accounts/{account}/workers/scripts/{name}[/settings | /content/v2 | /secrets[/{secret}]]
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
		// 実 API と同じく multipart で返し、メインモジュール名をヘッダで示す
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		part, _ := mw.CreateFormFile(existing.meta.MainModule, existing.meta.MainModule)
		_, _ = part.Write([]byte(existing.content))
		_ = mw.Close()
		w.Header().Set("Content-Type", mw.FormDataContentType())
		w.Header().Set("CF-Entrypoint", existing.meta.MainModule)
		_, _ = w.Write(buf.Bytes())
	case sub == "secrets" && r.Method == http.MethodPut:
		var b client.WorkerBinding
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
			f.writeResult(w, http.StatusBadRequest, nil)
			return
		}
		bindings := []client.WorkerBinding{}
		for _, cur := range existing.meta.Bindings {
			if cur.Name != b.Name {
				bindings = append(bindings, cur)
			}
		}
		existing.meta.Bindings = append(bindings, b)
		f.scripts[key] = existing
		f.secretPuts = append(f.secretPuts, b.Name)
		f.writeResult(w, http.StatusOK, map[string]string{"name": b.Name, "type": b.Type})
	case strings.HasPrefix(sub, "secrets/") && r.Method == http.MethodDelete:
		name := strings.TrimPrefix(sub, "secrets/")
		bindings := []client.WorkerBinding{}
		for _, cur := range existing.meta.Bindings {
			if !(cur.Type == client.WorkerBindingSecretText && cur.Name == name) {
				bindings = append(bindings, cur)
			}
		}
		existing.meta.Bindings = bindings
		f.scripts[key] = existing
		f.writeResult(w, http.StatusOK, nil)
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
	fake := newFakeWorkers()
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
				// main_module が既定値以外でも import で復元できる
				ResourceName:      addr,
				ImportState:       true,
				ImportStateId:     "acct1/hello",
				ImportStateVerify: true,
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
	fake := newFakeWorkers()
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
				ResourceName:            "cloudflare_workers_script.test",
				ImportState:             true,
				ImportStateId:           accountID + "/tf-acc-test",
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"secret_text_bindings"},
			},
			{
				Config: config("v2"),
				Check:  resource.TestCheckResourceAttr("cloudflare_workers_script.test", "plain_text_bindings.MESSAGE", "v2"),
			},
		},
	})
}

func workersScriptNoContentConfig(secrets string) string {
	return `
resource "cloudflare_workers_script" "app" {
  account_id  = "acct1"
  script_name = "app"
  secret_text_bindings = {` + secrets + `
  }
}
`
}

// TestWorkersScriptResource_noContent は content を管理しないモードを確認する。
// 別の仕組み（Workers Builds の wrangler deploy）がコードとバインディングを変えても差分にせず、
// Secret だけを 1 件ずつ反映する。
func TestWorkersScriptResource_noContent(t *testing.T) {
	fake := newFakeWorkers()
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	t.Setenv(envAPIToken, "fake-token")
	t.Setenv(envBaseURL, srv.URL)

	const addr = "cloudflare_workers_script.app"
	const deployed = "export default { fetch() { return new Response('deployed by wrangler') } }"
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// 無ければ仮のスクリプトで作る
				Config: workersScriptNoContentConfig(`
    AUTH_SECRET = "s1"
    API_KEY     = "k1"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr(addr, "content"),
					resource.TestCheckResourceAttr(addr, "compatibility_date", fakeDefaultCompatibilityDate),
					resource.TestCheckResourceAttr(addr, "secret_text_bindings.AUTH_SECRET", "s1"),
					func(_ *terraform.State) error {
						fake.mu.Lock()
						defer fake.mu.Unlock()
						s := fake.scripts["acct1/app"]
						if s.content != placeholderContent || len(s.meta.Bindings) != 2 {
							return fmt.Errorf("unexpected placeholder: %+v", s)
						}
						return nil
					},
				),
			},
			{
				// wrangler deploy がコード・平文の変数・D1・互換性設定を置き換えても差分にしない
				PreConfig: func() {
					fake.mu.Lock()
					defer fake.mu.Unlock()
					s := fake.scripts["acct1/app"]
					s.content = deployed
					s.meta.MainModule = "index.js"
					s.meta.CompatibilityDate = "2026-09-11"
					s.meta.CompatibilityFlags = []string{"nodejs_compat"}
					s.meta.Bindings = append(s.meta.Bindings,
						client.WorkerBinding{Type: client.WorkerBindingPlainText, Name: "AUTH_TRUST_HOST", Text: "true"},
						client.WorkerBinding{Type: "d1", Name: "DB"},
					)
					fake.scripts["acct1/app"] = s
				},
				Config: workersScriptNoContentConfig(`
    AUTH_SECRET = "s1"
    API_KEY     = "k1"`),
				PlanOnly: true,
			},
			{
				// 変わった Secret だけを送り、消えたものは消す。コードと他のバインディングには触らない
				PreConfig: func() {
					fake.mu.Lock()
					defer fake.mu.Unlock()
					fake.secretPuts = nil
				},
				Config: workersScriptNoContentConfig(`
    AUTH_SECRET = "s2"
    NEW_SECRET  = "n1"`),
				Check: func(_ *terraform.State) error {
					fake.mu.Lock()
					defer fake.mu.Unlock()
					s := fake.scripts["acct1/app"]
					names := map[string]string{}
					for _, b := range s.meta.Bindings {
						names[b.Name] = b.Type
					}
					if fmt.Sprint(fake.secretPuts) != "[AUTH_SECRET NEW_SECRET]" {
						return fmt.Errorf("unexpected secret updates: %v", fake.secretPuts)
					}
					if s.content != deployed || names["DB"] != "d1" || names["AUTH_TRUST_HOST"] != client.WorkerBindingPlainText || names["API_KEY"] != "" {
						return fmt.Errorf("unexpected script after secret sync: %+v", s)
					}
					return nil
				},
			},
			{
				ResourceName:      addr,
				ImportState:       true,
				ImportStateId:     "acct1/app/no-content",
				ImportStateVerify: true,
				// secret_text の値は API から取得できない
				ImportStateVerifyIgnore: []string{"secret_text_bindings"},
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

// TestWorkersScriptResource_noContentExisting は、content を管理しないモードで既存の Worker を
// 黙って取り込まず、import を促すことを確認する。
func TestWorkersScriptResource_noContentExisting(t *testing.T) {
	fake := newFakeWorkers()
	fake.scripts["acct1/app"] = fakeWorkerScript{meta: client.WorkerScriptMetadata{MainModule: "index.js"}, content: "export default {}"}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	t.Setenv(envAPIToken, "fake-token")
	t.Setenv(envBaseURL, srv.URL)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      workersScriptNoContentConfig(`A = "1"`),
				ExpectError: regexp.MustCompile(`acct1/app/no-content`),
			},
		},
	})
}

func TestWorkersScriptResource_noContentConflicts(t *testing.T) {
	t.Setenv(envAPIToken, "fake-token")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: `
resource "cloudflare_workers_script" "app" {
  account_id          = "acct1"
  script_name         = "app"
  plain_text_bindings = { A = "1" }
  compatibility_date  = "2026-01-01"
}
`,
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`(?s)plain_text_bindings は content を指定したときだけ.*compatibility_date は content`),
			},
		},
	})
}
