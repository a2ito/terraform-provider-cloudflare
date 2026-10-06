package client

import (
	"context"
	"net/http"
	"testing"
)

func TestTokensPath(t *testing.T) {
	cases := []struct {
		accountID, want string
	}{
		{"", "/user/tokens"},
		{"acct1", "/accounts/acct1/tokens"},
	}
	for _, tc := range cases {
		if got := tokensPath(tc.accountID); got != tc.want {
			t.Errorf("tokensPath(%q) = %q, want %q", tc.accountID, got, tc.want)
		}
	}
}

func TestCreateAPITokenReturnsValue(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/accounts/acct1/tokens" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"success":true,"errors":[],"result":{
			"id":"t1","name":"ci","status":"active","value":"secret-value",
			"policies":[{"id":"p1","effect":"allow","permission_groups":[{"id":"g1","name":"DNS Write"}],
			             "resources":{"com.cloudflare.api.account.acct1":{"com.cloudflare.api.account.zone.*":"*"}}}]}}`))
	})

	got, err := c.CreateAPIToken(context.Background(), "acct1", APIToken{Name: "ci"})
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	if got.Value != "secret-value" || len(got.Policies) != 1 || got.Policies[0].PermissionGroups[0].ID != "g1" {
		t.Errorf("unexpected result: %+v", got)
	}
}

func TestListPermissionGroups(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/user/tokens/permission_groups" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"success":true,"errors":[],"result":[{"id":"g1","name":"DNS Write","scopes":["com.cloudflare.api.account.zone"]}]}`))
	})

	got, err := c.ListPermissionGroups(context.Background(), "")
	if err != nil {
		t.Fatalf("ListPermissionGroups: %v", err)
	}
	if len(got) != 1 || got[0].Name != "DNS Write" || got[0].Scopes[0] != "com.cloudflare.api.account.zone" {
		t.Errorf("unexpected result: %+v", got)
	}
}
