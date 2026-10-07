package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/a2ito/terraform-provider-cloudflare/internal/client"
)

const fakeBuildsToken = "fake-builds-token"

func TestParseBuildTriggerImportID(t *testing.T) {
	accountID, name, uuid, err := parseBuildTriggerImportID("acct1/hello/t1")
	if err != nil || accountID != "acct1" || name != "hello" || uuid != "t1" {
		t.Errorf("parseBuildTriggerImportID(acct1/hello/t1) = (%q, %q, %q, %v)", accountID, name, uuid, err)
	}
	for _, bad := range []string{"", "acct1", "acct1/hello", "acct1/hello/", "/hello/t1", "a/b/c/d"} {
		if _, _, _, err := parseBuildTriggerImportID(bad); err == nil {
			t.Errorf("parseBuildTriggerImportID(%q) succeeded, want error", bad)
		}
	}
}

// serveBuilds は Workers Builds の API（/accounts/{account}/builds/...）を模す。parts は builds より後ろ。
func (f *fakeWorkers) serveBuilds(w http.ResponseWriter, r *http.Request, parts []string) {
	// 実 API と同じく、アカウントのトークン（ここでは通常のトークン）は受け付けない
	if r.Header.Get("Authorization") != "Bearer "+fakeBuildsToken {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": false, "errors": []client.APIError{{Code: 12006, Message: "Invalid token"}}, "result": nil,
		})
		return
	}

	switch {
	// workers/{tag}/triggers
	case len(parts) == 3 && parts[0] == "workers" && parts[2] == "triggers" && r.Method == http.MethodGet:
		list := []client.BuildTrigger{}
		for _, t := range f.triggers {
			if t.ExternalScriptID == parts[1] {
				list = append(list, t)
			}
		}
		f.writeResult(w, http.StatusOK, list)

	case len(parts) == 1 && parts[0] == "triggers" && r.Method == http.MethodPost:
		var t client.BuildTrigger
		// 実 API と同じく、trigger_name が無いと 12002: Invalid request body
		if err := json.NewDecoder(r.Body).Decode(&t); err != nil || t.ExternalScriptID == "" || t.RepoConnectionUUID == "" || t.TriggerName == "" {
			f.writeResult(w, http.StatusBadRequest, nil)
			return
		}
		f.nextID++
		t.TriggerUUID = fmt.Sprintf("trigger-%d", f.nextID)
		if t.BuildCachingEnabled == nil {
			disabled := false
			t.BuildCachingEnabled = &disabled
		}
		// 実 API と同じく、接続はネストしたオブジェクトで返す
		t.RepoConnection = &struct {
			RepoConnectionUUID string `json:"repo_connection_uuid"`
		}{RepoConnectionUUID: t.RepoConnectionUUID}
		t.RepoConnectionUUID = ""
		f.triggers[t.TriggerUUID] = t
		f.buildEnv[t.TriggerUUID] = map[string]client.BuildEnvironmentVariable{}
		f.writeResult(w, http.StatusOK, t)

	case len(parts) >= 2 && parts[0] == "triggers":
		uuid := parts[1]
		existing, ok := f.triggers[uuid]
		if !ok {
			f.writeResult(w, http.StatusNotFound, nil)
			return
		}
		sub := strings.Join(parts[2:], "/")
		switch {
		case sub == "" && r.Method == http.MethodPatch:
			var t client.BuildTrigger
			if err := json.NewDecoder(r.Body).Decode(&t); err != nil {
				f.writeResult(w, http.StatusBadRequest, nil)
				return
			}
			t.TriggerUUID, t.ExternalScriptID, t.RepoConnection = uuid, existing.ExternalScriptID, existing.RepoConnection
			if t.TriggerName == "" {
				t.TriggerName = existing.TriggerName
			}
			if t.BuildCachingEnabled == nil {
				t.BuildCachingEnabled = existing.BuildCachingEnabled
			}
			f.triggers[uuid] = t
			f.writeResult(w, http.StatusOK, t)
		case sub == "" && r.Method == http.MethodDelete:
			delete(f.triggers, uuid)
			delete(f.buildEnv, uuid)
			f.writeResult(w, http.StatusOK, nil)
		case sub == "environment_variables" && r.Method == http.MethodGet:
			// secret の値は返さない
			out := map[string]client.BuildEnvironmentVariable{}
			for k, v := range f.buildEnv[uuid] {
				if v.IsSecret {
					v.Value = ""
				}
				out[k] = v
			}
			f.writeResult(w, http.StatusOK, out)
		case sub == "environment_variables" && r.Method == http.MethodPatch:
			var vars map[string]client.BuildEnvironmentVariable
			if err := json.NewDecoder(r.Body).Decode(&vars); err != nil {
				f.writeResult(w, http.StatusBadRequest, nil)
				return
			}
			for k, v := range vars {
				f.buildEnv[uuid][k] = v
			}
			f.writeResult(w, http.StatusOK, nil)
		case strings.HasPrefix(sub, "environment_variables/") && r.Method == http.MethodDelete:
			key := strings.TrimPrefix(sub, "environment_variables/")
			if _, ok := f.buildEnv[uuid][key]; !ok {
				// 実 API と同じく、無いものを消すと 404 ではなく 12021 が返る
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"success": false, "errors": []client.APIError{{Code: 12021, Message: "no environment variable was found to delete by that key"}}, "result": nil,
				})
				return
			}
			delete(f.buildEnv[uuid], key)
			f.writeResult(w, http.StatusOK, nil)
		default:
			f.writeResult(w, http.StatusMethodNotAllowed, nil)
		}

	default:
		f.writeResult(w, http.StatusNotFound, nil)
	}
}

func buildTriggerConfig(body string) string {
	return `
resource "cloudflare_workers_script" "app" {
  account_id  = "acct1"
  script_name = "app"
}

resource "cloudflare_workers_build_trigger" "app" {
  account_id           = "acct1"
  script_name          = cloudflare_workers_script.app.script_name
  repo_connection_uuid = "repo-1"
  build_command        = "npm run cf:build"
  deploy_command       = "npm run cf:deploy"
  root_directory       = "apps/app"
  branch_includes      = ["main"]
` + body + `
}
`
}

// TestWorkersBuildTriggerResource_fake はフェイクサーバを相手に、トリガーの CRUD・import・Build variables・
// Terraform 外での削除からの復旧を通しで確認する。
func TestWorkersBuildTriggerResource_fake(t *testing.T) {
	fake := newFakeWorkers()
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	t.Setenv(envAPIToken, "fake-token")
	t.Setenv(envBuildsAPIToken, fakeBuildsToken)
	t.Setenv(envBaseURL, srv.URL)

	const addr = "cloudflare_workers_build_trigger.app"
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: buildTriggerConfig(`
  build_token_uuid = "token-1"
  path_includes    = ["apps/app/*", "package-lock.json"]
  environment_variables = {
    APP_HOSTNAME   = "app.example.com"
    D1_DATABASE_ID = "db-1"
  }
  secret_environment_variables = {
    NPM_TOKEN = "s1"
  }
`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "id", "trigger-1"),
					resource.TestCheckResourceAttr(addr, "script_tag", fakeTag("app")),
					resource.TestCheckResourceAttr(addr, "trigger_name", fakeTag("app")),
					resource.TestCheckResourceAttr(addr, "build_caching_enabled", "false"),
					resource.TestCheckResourceAttr(addr, "repo_connection_uuid", "repo-1"),
					resource.TestCheckResourceAttr(addr, "environment_variables.APP_HOSTNAME", "app.example.com"),
					resource.TestCheckResourceAttr(addr, "secret_environment_variables.NPM_TOKEN", "s1"),
				),
			},
			{
				ResourceName:      addr,
				ImportState:       true,
				ImportStateId:     "acct1/app/trigger-1",
				ImportStateVerify: true,
				// secret の値は API から取得できない
				ImportStateVerifyIgnore: []string{"secret_environment_variables"},
			},
			{
				// パスの順序を変えても差分にならない。変数の追加・変更・削除、キャッシュ・トークンの変更を反映する。
				// Terraform の外で消された変数（NPM_TOKEN）は refresh で state から外れ、削除を試みない
				PreConfig: func() {
					fake.mu.Lock()
					defer fake.mu.Unlock()
					delete(fake.buildEnv["trigger-1"], "NPM_TOKEN")
				},
				Config: buildTriggerConfig(`
  build_token_uuid      = "token-2"
  build_caching_enabled = true
  path_includes         = ["package-lock.json", "apps/app/*"]
  environment_variables = {
    APP_HOSTNAME = "app2.example.com"
    NEW_VAR      = "x"
  }
`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "id", "trigger-1"),
					resource.TestCheckResourceAttr(addr, "build_caching_enabled", "true"),
					resource.TestCheckNoResourceAttr(addr, "secret_environment_variables"),
					func(_ *terraform.State) error {
						fake.mu.Lock()
						defer fake.mu.Unlock()
						tr, env := fake.triggers["trigger-1"], fake.buildEnv["trigger-1"]
						if tr.BuildTokenUUID != "token-2" || len(env) != 2 || env["APP_HOSTNAME"].Value != "app2.example.com" || env["NEW_VAR"].Value != "x" {
							return fmt.Errorf("unexpected trigger: %+v env: %+v", tr, env)
						}
						return nil
					},
				),
			},
			{
				// Terraform の外でトリガーが消されたら作り直す
				PreConfig: func() {
					fake.mu.Lock()
					defer fake.mu.Unlock()
					delete(fake.triggers, "trigger-1")
				},
				Config: buildTriggerConfig(`
  build_token_uuid      = "token-2"
  build_caching_enabled = true
  path_includes         = ["package-lock.json", "apps/app/*"]
  environment_variables = {
    APP_HOSTNAME = "app2.example.com"
    NEW_VAR      = "x"
  }
`),
				Check: resource.TestCheckResourceAttr(addr, "id", "trigger-2"),
			},
		},
		CheckDestroy: func(_ *terraform.State) error {
			fake.mu.Lock()
			defer fake.mu.Unlock()
			if len(fake.triggers) != 0 || len(fake.scripts) != 0 {
				return fmt.Errorf("resources still exist after destroy: triggers=%v scripts=%v", fake.triggers, fake.scripts)
			}
			return nil
		},
	})
}

func TestWorkersBuildTriggerResource_duplicateEnvironmentVariable(t *testing.T) {
	t.Setenv(envAPIToken, "fake-token")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: buildTriggerConfig(`
  build_token_uuid             = "token-1"
  environment_variables        = { NAME = "a" }
  secret_environment_variables = { NAME = "b" }
`),
				PlanOnly:    true,
				ExpectError: regexp.MustCompile("Duplicate environment variable"),
			},
		},
	})
}

func TestWorkersBuildTriggerResource_invalidImportID(t *testing.T) {
	t.Setenv(envAPIToken, "fake-token")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:        `resource "cloudflare_workers_build_trigger" "test" {}`,
				ResourceName:  "cloudflare_workers_build_trigger.test",
				ImportState:   true,
				ImportStateId: "acct1/app",
				ExpectError:   regexp.MustCompile("<account_id>/<script_name>/<trigger_uuid>"),
			},
		},
	})
}

// TestAccWorkersBuildTriggerResource は実際の Cloudflare に対して実行する。
// content を管理しない Worker を作り、存在しないブランチだけをビルドするトリガーを付けて消す（ビルドは走らない）。
// Worker の Secret の更新（secrets API）もここで確かめる。
// TF_ACC=1 と CLOUDFLARE_API_TOKEN / CLOUDFLARE_BUILDS_API_TOKEN / CLOUDFLARE_ACCOUNT_ID、
// 既存の接続とビルドトークンの CLOUDFLARE_REPO_CONNECTION_UUID / CLOUDFLARE_BUILD_TOKEN_UUID が必要。
func TestAccWorkersBuildTriggerResource(t *testing.T) {
	accountID := os.Getenv("CLOUDFLARE_ACCOUNT_ID")
	repo, token := os.Getenv("CLOUDFLARE_REPO_CONNECTION_UUID"), os.Getenv("CLOUDFLARE_BUILD_TOKEN_UUID")
	if os.Getenv("TF_ACC") != "" && (accountID == "" || repo == "" || token == "" || os.Getenv(envBuildsAPIToken) == "") {
		t.Fatal("CLOUDFLARE_ACCOUNT_ID / CLOUDFLARE_BUILDS_API_TOKEN / CLOUDFLARE_REPO_CONNECTION_UUID / CLOUDFLARE_BUILD_TOKEN_UUID を設定してください")
	}

	config := func(value string) string {
		return fmt.Sprintf(`
resource "cloudflare_workers_script" "test" {
  account_id           = %[1]q
  script_name          = "tf-acc-test-builds"
  secret_text_bindings = { TF_ACC_SECRET = %[4]q }
}

resource "cloudflare_workers_build_trigger" "test" {
  account_id           = %[1]q
  script_name          = cloudflare_workers_script.test.script_name
  repo_connection_uuid = %[2]q
  build_token_uuid     = %[3]q
  build_command        = "true"
  deploy_command       = "true"
  branch_includes      = ["tf-acc-test-never-pushed"]
  path_includes        = ["tf-acc-test/*"]

  environment_variables = { TF_ACC_TEST = %[4]q }
}
`, accountID, repo, token, value)
	}

	const addr = "cloudflare_workers_build_trigger.test"
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config("v1"),
				Check:  resource.TestCheckResourceAttr(addr, "environment_variables.TF_ACC_TEST", "v1"),
			},
			{
				ResourceName: addr,
				ImportState:  true,
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					rs := s.RootModule().Resources[addr]
					return accountID + "/tf-acc-test-builds/" + rs.Primary.ID, nil
				},
				ImportStateVerify: true,
			},
			{
				Config: config("v2"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "environment_variables.TF_ACC_TEST", "v2"),
					resource.TestCheckResourceAttr("cloudflare_workers_script.test", "secret_text_bindings.TF_ACC_SECRET", "v2"),
				),
			},
		},
	})
}
