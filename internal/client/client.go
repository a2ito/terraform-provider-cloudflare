// Package client は Cloudflare API v4 の最小限の HTTP クライアント。
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const DefaultBaseURL = "https://api.cloudflare.com/client/v4"

type Client struct {
	baseURL    string
	apiToken   string
	httpClient *http.Client
}

type Option func(*Client)

func WithBaseURL(baseURL string) Option {
	return func(c *Client) { c.baseURL = strings.TrimRight(baseURL, "/") }
}

func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) { c.httpClient = hc }
}

func New(apiToken string, opts ...Option) *Client {
	c := &Client{
		baseURL:    DefaultBaseURL,
		apiToken:   apiToken,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// APIError は Cloudflare API が返すエラー 1 件。
type APIError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// ResponseError は API 呼び出しが失敗したことを表す。
type ResponseError struct {
	StatusCode int
	Errors     []APIError
}

func (e *ResponseError) Error() string {
	if len(e.Errors) == 0 {
		return fmt.Sprintf("cloudflare API returned HTTP %d", e.StatusCode)
	}
	msgs := make([]string, 0, len(e.Errors))
	for _, ae := range e.Errors {
		msgs = append(msgs, fmt.Sprintf("%d: %s", ae.Code, ae.Message))
	}
	return fmt.Sprintf("cloudflare API returned HTTP %d: %s", e.StatusCode, strings.Join(msgs, "; "))
}

// IsNotFound は err が 404 を表すかどうかを返す。
func IsNotFound(err error) bool {
	var re *ResponseError
	return errors.As(err, &re) && re.StatusCode == http.StatusNotFound
}

type envelope[T any] struct {
	Success bool       `json:"success"`
	Errors  []APIError `json:"errors"`
	Result  T          `json:"result"`
}

func do[T any](ctx context.Context, c *Client, method, path string, body any) (T, error) {
	var zero T

	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return zero, fmt.Errorf("marshal request body: %w", err)
		}
		reqBody = bytes.NewReader(b)
	}
	return send[T](ctx, c, method, path, reqBody, "application/json")
}

// send はリクエストを送り、Cloudflare API のエンベロープ形式のレスポンスから result を取り出す。
func send[T any](ctx context.Context, c *Client, method, path string, body io.Reader, contentType string) (T, error) {
	var zero T

	status, raw, err := c.roundTrip(ctx, method, path, body, contentType)
	if err != nil {
		return zero, err
	}

	var env envelope[T]
	if err := json.Unmarshal(raw, &env); err != nil {
		if status >= 400 {
			return zero, &ResponseError{StatusCode: status}
		}
		return zero, fmt.Errorf("decode response body (HTTP %d): %w", status, err)
	}

	if status >= 400 || !env.Success {
		return zero, &ResponseError{StatusCode: status, Errors: env.Errors}
	}
	return env.Result, nil
}

// getRaw はエンベロープに包まれていないレスポンス本文（スクリプト本体など）を取得する。
func getRaw(ctx context.Context, c *Client, path string) ([]byte, error) {
	status, raw, err := c.roundTrip(ctx, http.MethodGet, path, nil, "")
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		var env envelope[json.RawMessage]
		_ = json.Unmarshal(raw, &env) // エラー時のみエンベロープで返るので、読めなければ HTTP ステータスだけで報告する
		return nil, &ResponseError{StatusCode: status, Errors: env.Errors}
	}
	return raw, nil
}

func (c *Client) roundTrip(ctx context.Context, method, path string, body io.Reader, contentType string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return 0, nil, fmt.Errorf("build request %s %s: %w", method, path, err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiToken)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("send request %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, nil, fmt.Errorf("read response body: %w", err)
	}
	return resp.StatusCode, raw, nil
}
