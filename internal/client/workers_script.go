package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
)

const (
	WorkerBindingPlainText  = "plain_text"
	WorkerBindingSecretText = "secret_text"
)

// WorkerBinding は Worker のバインディング 1 件。
// secret_text の Text は取得時には返らない。
type WorkerBinding struct {
	Type string `json:"type"`
	Name string `json:"name"`
	Text string `json:"text,omitempty"`
}

// WorkerScriptMetadata はアップロード時の multipart の metadata パート。
type WorkerScriptMetadata struct {
	MainModule         string          `json:"main_module"`
	CompatibilityDate  string          `json:"compatibility_date,omitempty"`
	CompatibilityFlags []string        `json:"compatibility_flags,omitempty"`
	Bindings           []WorkerBinding `json:"bindings"`
}

type WorkerScript struct {
	ID         string `json:"id"`
	ETag       string `json:"etag"`
	CreatedOn  string `json:"created_on"`
	ModifiedOn string `json:"modified_on"`
}

type WorkerScriptSettings struct {
	CompatibilityDate  string          `json:"compatibility_date"`
	CompatibilityFlags []string        `json:"compatibility_flags"`
	Bindings           []WorkerBinding `json:"bindings"`
}

func workerScriptPath(accountID, scriptName string) string {
	return fmt.Sprintf("/accounts/%s/workers/scripts/%s", url.PathEscape(accountID), url.PathEscape(scriptName))
}

// buildWorkerScriptForm は ES Modules 形式のスクリプト 1 ファイルと metadata から multipart の本文を作る。
func buildWorkerScriptForm(meta WorkerScriptMetadata, content string) (*bytes.Buffer, string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)

	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return nil, "", fmt.Errorf("marshal worker metadata: %w", err)
	}
	metaPart, err := w.CreatePart(textproto.MIMEHeader{
		"Content-Disposition": {`form-data; name="metadata"`},
		"Content-Type":        {"application/json"},
	})
	if err != nil {
		return nil, "", fmt.Errorf("create metadata part: %w", err)
	}
	if _, err := metaPart.Write(metaJSON); err != nil {
		return nil, "", fmt.Errorf("write metadata part: %w", err)
	}

	modulePart, err := w.CreatePart(textproto.MIMEHeader{
		"Content-Disposition": {multipart.FileContentDisposition(meta.MainModule, meta.MainModule)},
		"Content-Type":        {"application/javascript+module"},
	})
	if err != nil {
		return nil, "", fmt.Errorf("create module part: %w", err)
	}
	if _, err := modulePart.Write([]byte(content)); err != nil {
		return nil, "", fmt.Errorf("write module part: %w", err)
	}

	if err := w.Close(); err != nil {
		return nil, "", fmt.Errorf("close multipart writer: %w", err)
	}
	return &buf, w.FormDataContentType(), nil
}

// UploadWorkerScript はスクリプトを作成または丸ごと置き換える（デプロイも同時に行われる）。
func (c *Client) UploadWorkerScript(ctx context.Context, accountID, scriptName string, meta WorkerScriptMetadata, content string) (WorkerScript, error) {
	body, contentType, err := buildWorkerScriptForm(meta, content)
	if err != nil {
		return WorkerScript{}, err
	}
	return send[WorkerScript](ctx, c, http.MethodPut, workerScriptPath(accountID, scriptName), body, contentType)
}

func (c *Client) GetWorkerScriptSettings(ctx context.Context, accountID, scriptName string) (WorkerScriptSettings, error) {
	return do[WorkerScriptSettings](ctx, c, http.MethodGet, workerScriptPath(accountID, scriptName)+"/settings", nil)
}

// WorkerScriptContent はアップロード済みのメインモジュール。
type WorkerScriptContent struct {
	MainModule string
	Content    string
}

// GetWorkerScriptContent はメインモジュールの名前と中身を取得する。
// API はモジュールを multipart で返し、メインモジュール名を CF-Entrypoint ヘッダで示す。
func (c *Client) GetWorkerScriptContent(ctx context.Context, accountID, scriptName string) (WorkerScriptContent, error) {
	header, raw, err := getRaw(ctx, c, workerScriptPath(accountID, scriptName)+"/content/v2")
	if err != nil {
		return WorkerScriptContent{}, err
	}
	return parseWorkerScriptContent(header, raw)
}

func parseWorkerScriptContent(header http.Header, raw []byte) (WorkerScriptContent, error) {
	entrypoint := header.Get("CF-Entrypoint")
	mediaType, params, err := mime.ParseMediaType(header.Get("Content-Type"))
	if err != nil || !strings.HasPrefix(mediaType, "multipart/") {
		return WorkerScriptContent{}, fmt.Errorf("unexpected Content-Type %q for worker script content", header.Get("Content-Type"))
	}

	r := multipart.NewReader(bytes.NewReader(raw), params["boundary"])
	for {
		p, err := r.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return WorkerScriptContent{}, fmt.Errorf("read worker script content: %w", err)
		}
		if entrypoint != "" && p.FormName() != entrypoint {
			continue
		}
		b, err := io.ReadAll(p)
		if err != nil {
			return WorkerScriptContent{}, fmt.Errorf("read worker module %q: %w", p.FormName(), err)
		}
		return WorkerScriptContent{MainModule: p.FormName(), Content: string(b)}, nil
	}
	return WorkerScriptContent{}, fmt.Errorf("main module %q not found in worker script content", entrypoint)
}

func (c *Client) DeleteWorkerScript(ctx context.Context, accountID, scriptName string) error {
	_, err := do[json.RawMessage](ctx, c, http.MethodDelete, workerScriptPath(accountID, scriptName), nil)
	return err
}

// WorkerScriptSummary はアカウントの Worker 一覧の 1 件。
type WorkerScriptSummary struct {
	ID  string `json:"id"`
	Tag string `json:"tag"`
}

// GetWorkerScriptTag はスクリプト名から Worker の tag（Builds の API が使う不変の ID）を引く。
// 見つからなければ 404 の ResponseError を返す。
func (c *Client) GetWorkerScriptTag(ctx context.Context, accountID, scriptName string) (string, error) {
	scripts, err := do[[]WorkerScriptSummary](ctx, c, http.MethodGet, fmt.Sprintf("/accounts/%s/workers/scripts", url.PathEscape(accountID)), nil)
	if err != nil {
		return "", err
	}
	for _, s := range scripts {
		if s.ID == scriptName {
			return s.Tag, nil
		}
	}
	return "", &ResponseError{StatusCode: http.StatusNotFound, Errors: []APIError{{Message: fmt.Sprintf("worker script %q not found", scriptName)}}}
}

// PutWorkerSecret は Secret を 1 件作成または更新する。他のバインディングとコードには触らない。
func (c *Client) PutWorkerSecret(ctx context.Context, accountID, scriptName, name, text string) error {
	_, err := do[json.RawMessage](ctx, c, http.MethodPut, workerScriptPath(accountID, scriptName)+"/secrets",
		WorkerBinding{Type: WorkerBindingSecretText, Name: name, Text: text})
	return err
}

func (c *Client) DeleteWorkerSecret(ctx context.Context, accountID, scriptName, name string) error {
	_, err := do[json.RawMessage](ctx, c, http.MethodDelete, workerScriptPath(accountID, scriptName)+"/secrets/"+url.PathEscape(name), nil)
	return err
}
