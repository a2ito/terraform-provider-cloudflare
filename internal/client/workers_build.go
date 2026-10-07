package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
)

// BuildTrigger は Workers Builds のトリガー（どのブランチ・パスの変更で、何を実行してデプロイするか）。
type BuildTrigger struct {
	TriggerUUID         string   `json:"trigger_uuid,omitempty"`
	ExternalScriptID    string   `json:"external_script_id,omitempty"`
	RepoConnectionUUID  string   `json:"repo_connection_uuid,omitempty"`
	BuildTokenUUID      string   `json:"build_token_uuid,omitempty"`
	TriggerName         string   `json:"trigger_name,omitempty"`
	BuildCommand        string   `json:"build_command"`
	DeployCommand       string   `json:"deploy_command"`
	RootDirectory       string   `json:"root_directory"`
	BranchIncludes      []string `json:"branch_includes"`
	BranchExcludes      []string `json:"branch_excludes"`
	PathIncludes        []string `json:"path_includes"`
	PathExcludes        []string `json:"path_excludes"`
	BuildCachingEnabled *bool    `json:"build_caching_enabled,omitempty"`

	// RepoConnection は取得時にだけ返る。
	RepoConnection *struct {
		RepoConnectionUUID string `json:"repo_connection_uuid"`
	} `json:"repo_connection,omitempty"`
}

// BuildEnvironmentVariable は Build variable 1 件。secret の値は取得時には返らない。
type BuildEnvironmentVariable struct {
	Value    string `json:"value,omitempty"`
	IsSecret bool   `json:"is_secret"`
}

func buildsPath(accountID string) string {
	return fmt.Sprintf("/accounts/%s/builds", url.PathEscape(accountID))
}

func buildTriggerPath(accountID, triggerUUID string) string {
	return buildsPath(accountID) + "/triggers/" + url.PathEscape(triggerUUID)
}

// ListBuildTriggers は Worker（tag で指定）のトリガーを返す。
func (c *Client) ListBuildTriggers(ctx context.Context, accountID, scriptTag string) ([]BuildTrigger, error) {
	return do[[]BuildTrigger](ctx, c.builds(), http.MethodGet, buildsPath(accountID)+"/workers/"+url.PathEscape(scriptTag)+"/triggers", nil)
}

// GetBuildTrigger は Worker のトリガーから UUID が一致するものを返す。
// 単体を取得する API が無いため、一覧から探す。見つからなければ 404 の ResponseError を返す。
func (c *Client) GetBuildTrigger(ctx context.Context, accountID, scriptTag, triggerUUID string) (BuildTrigger, error) {
	triggers, err := c.ListBuildTriggers(ctx, accountID, scriptTag)
	if err != nil {
		return BuildTrigger{}, err
	}
	for _, t := range triggers {
		if t.TriggerUUID == triggerUUID {
			return t, nil
		}
	}
	return BuildTrigger{}, &ResponseError{StatusCode: http.StatusNotFound, Errors: []APIError{{Message: fmt.Sprintf("build trigger %q not found", triggerUUID)}}}
}

func (c *Client) CreateBuildTrigger(ctx context.Context, accountID string, t BuildTrigger) (BuildTrigger, error) {
	return do[BuildTrigger](ctx, c.builds(), http.MethodPost, buildsPath(accountID)+"/triggers", t)
}

func (c *Client) UpdateBuildTrigger(ctx context.Context, accountID, triggerUUID string, t BuildTrigger) (BuildTrigger, error) {
	return do[BuildTrigger](ctx, c.builds(), http.MethodPatch, buildTriggerPath(accountID, triggerUUID), t)
}

func (c *Client) DeleteBuildTrigger(ctx context.Context, accountID, triggerUUID string) error {
	_, err := do[json.RawMessage](ctx, c.builds(), http.MethodDelete, buildTriggerPath(accountID, triggerUUID), nil)
	return err
}

func (c *Client) ListBuildEnvironmentVariables(ctx context.Context, accountID, triggerUUID string) (map[string]BuildEnvironmentVariable, error) {
	return do[map[string]BuildEnvironmentVariable](ctx, c.builds(), http.MethodGet, buildTriggerPath(accountID, triggerUUID)+"/environment_variables", nil)
}

// SetBuildEnvironmentVariables は渡したものだけを作成・更新する。渡さなかったものは残る。
func (c *Client) SetBuildEnvironmentVariables(ctx context.Context, accountID, triggerUUID string, vars map[string]BuildEnvironmentVariable) error {
	_, err := do[json.RawMessage](ctx, c.builds(), http.MethodPatch, buildTriggerPath(accountID, triggerUUID)+"/environment_variables", vars)
	return err
}

// errCodeBuildEnvironmentVariableNotFound は、無い Build variable を消そうとしたときのエラーコード。
// 404 ではなくこのコードで返るため、別に判定する。
const errCodeBuildEnvironmentVariableNotFound = 12021

// DeleteBuildEnvironmentVariable は Build variable を 1 件消す。既に無ければ何もしない。
func (c *Client) DeleteBuildEnvironmentVariable(ctx context.Context, accountID, triggerUUID, key string) error {
	_, err := do[json.RawMessage](ctx, c.builds(), http.MethodDelete, buildTriggerPath(accountID, triggerUUID)+"/environment_variables/"+url.PathEscape(key), nil)
	var re *ResponseError
	if errors.As(err, &re) {
		for _, e := range re.Errors {
			if e.Code == errCodeBuildEnvironmentVariableNotFound {
				return nil
			}
		}
	}
	return err
}
