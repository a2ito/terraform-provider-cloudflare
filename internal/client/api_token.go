package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

type PermissionGroupRef struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

type TokenPolicy struct {
	ID               string               `json:"id,omitempty"`
	Effect           string               `json:"effect"`
	PermissionGroups []PermissionGroupRef `json:"permission_groups"`
	// Resources はリソース名から "*" またはネストしたオブジェクトへの map。
	// アカウント配下の全 Zone のように入れ子になることがあるため JSON のまま扱う。
	Resources json.RawMessage `json:"resources"`
}

type TokenRequestIP struct {
	In    []string `json:"in,omitempty"`
	NotIn []string `json:"not_in,omitempty"`
}

type TokenCondition struct {
	RequestIP *TokenRequestIP `json:"request_ip,omitempty"`
}

type APIToken struct {
	ID         string          `json:"id,omitempty"`
	Name       string          `json:"name"`
	Status     string          `json:"status,omitempty"`
	IssuedOn   string          `json:"issued_on,omitempty"`
	ModifiedOn string          `json:"modified_on,omitempty"`
	NotBefore  string          `json:"not_before,omitempty"`
	ExpiresOn  string          `json:"expires_on,omitempty"`
	Policies   []TokenPolicy   `json:"policies"`
	Condition  *TokenCondition `json:"condition,omitempty"`
	// Value はトークンの secret。作成時のレスポンスにしか含まれない。
	Value string `json:"value,omitempty"`
}

type PermissionGroup struct {
	ID     string   `json:"id"`
	Name   string   `json:"name"`
	Scopes []string `json:"scopes"`
}

// tokensPath は accountID が空ならユーザーのトークン、そうでなければアカウントのトークンのパスを返す。
func tokensPath(accountID string) string {
	if accountID == "" {
		return "/user/tokens"
	}
	return fmt.Sprintf("/accounts/%s/tokens", url.PathEscape(accountID))
}

func tokenPath(accountID, tokenID string) string {
	return tokensPath(accountID) + "/" + url.PathEscape(tokenID)
}

func (c *Client) CreateAPIToken(ctx context.Context, accountID string, tok APIToken) (APIToken, error) {
	return do[APIToken](ctx, c, http.MethodPost, tokensPath(accountID), tok)
}

func (c *Client) GetAPIToken(ctx context.Context, accountID, tokenID string) (APIToken, error) {
	return do[APIToken](ctx, c, http.MethodGet, tokenPath(accountID, tokenID), nil)
}

// UpdateAPIToken はトークンを PUT で丸ごと置き換える。secret の値は変わらない。
func (c *Client) UpdateAPIToken(ctx context.Context, accountID, tokenID string, tok APIToken) (APIToken, error) {
	body, err := updateBody(tok)
	if err != nil {
		return APIToken{}, err
	}
	return do[APIToken](ctx, c, http.MethodPut, tokenPath(accountID, tokenID), body)
}

// updateBody は PUT 用のリクエストボディを作る。
// 空の expires_on / not_before は省略せず null を送り、既存の値を消す。
func updateBody(tok APIToken) (map[string]any, error) {
	tok.Value = ""
	b, err := json.Marshal(tok)
	if err != nil {
		return nil, fmt.Errorf("marshal api token: %w", err)
	}
	var body map[string]any
	if err := json.Unmarshal(b, &body); err != nil {
		return nil, fmt.Errorf("unmarshal api token: %w", err)
	}
	if tok.ExpiresOn == "" {
		body["expires_on"] = nil
	}
	if tok.NotBefore == "" {
		body["not_before"] = nil
	}
	return body, nil
}

func (c *Client) DeleteAPIToken(ctx context.Context, accountID, tokenID string) error {
	_, err := do[struct {
		ID string `json:"id"`
	}](ctx, c, http.MethodDelete, tokenPath(accountID, tokenID), nil)
	return err
}

func (c *Client) ListPermissionGroups(ctx context.Context, accountID string) ([]PermissionGroup, error) {
	return do[[]PermissionGroup](ctx, c, http.MethodGet, tokensPath(accountID)+"/permission_groups", nil)
}
