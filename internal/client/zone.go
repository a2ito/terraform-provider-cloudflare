package client

import (
	"context"
	"net/http"
	"net/url"
)

type Zone struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Status      string   `json:"status"`
	Paused      bool     `json:"paused"`
	NameServers []string `json:"name_servers"`
	Account     struct {
		ID string `json:"id"`
	} `json:"account"`
}

// ListZonesByName は名前が完全一致する Zone を返す。
func (c *Client) ListZonesByName(ctx context.Context, name string) ([]Zone, error) {
	q := url.Values{"name": {name}}
	return do[[]Zone](ctx, c, http.MethodGet, "/zones?"+q.Encode(), nil)
}
