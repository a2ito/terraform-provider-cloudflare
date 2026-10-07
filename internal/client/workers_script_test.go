package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"testing"
)

func TestUploadWorkerScriptSendsMultipart(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/accounts/acct1/workers/scripts/hello" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		mr, err := r.MultipartReader()
		if err != nil {
			t.Fatalf("MultipartReader: %v", err)
		}

		parts := map[string]string{}
		types := map[string]string{}
		for {
			p, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("NextPart: %v", err)
			}
			b, _ := io.ReadAll(p)
			parts[p.FormName()] = string(b)
			types[p.FormName()] = p.Header.Get("Content-Type")
		}

		var meta WorkerScriptMetadata
		if err := json.Unmarshal([]byte(parts["metadata"]), &meta); err != nil {
			t.Fatalf("decode metadata: %v", err)
		}
		if meta.MainModule != "worker.js" || meta.CompatibilityDate != "2026-01-01" || len(meta.Bindings) != 1 || meta.Bindings[0].Text != "hi" {
			t.Errorf("unexpected metadata: %+v", meta)
		}
		if parts["worker.js"] != "export default {}" || types["worker.js"] != "application/javascript+module" {
			t.Errorf("unexpected module part: %q (%s)", parts["worker.js"], types["worker.js"])
		}
		_, _ = w.Write([]byte(`{"success":true,"errors":[],"result":{"id":"hello","etag":"e1"}}`))
	})

	got, err := c.UploadWorkerScript(context.Background(), "acct1", "hello", WorkerScriptMetadata{
		MainModule:        "worker.js",
		CompatibilityDate: "2026-01-01",
		Bindings:          []WorkerBinding{{Type: WorkerBindingPlainText, Name: "MSG", Text: "hi"}},
	}, "export default {}")
	if err != nil {
		t.Fatalf("UploadWorkerScript: %v", err)
	}
	if got.ID != "hello" || got.ETag != "e1" {
		t.Errorf("unexpected result: %+v", got)
	}
}

func TestGetWorkerScriptContent(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/accounts/acct1/workers/scripts/hello/content/v2":
			// 実 API と同じく、モジュールを multipart で返しメインモジュールをヘッダで示す
			w.Header().Set("Content-Type", "multipart/form-data; boundary=b1")
			w.Header().Set("CF-Entrypoint", "index.mjs")
			_, _ = w.Write([]byte("--b1\r\n" +
				"Content-Disposition: form-data; name=\"util.mjs\"; filename=\"util.mjs\"\r\n" +
				"Content-Type: application/javascript+module\r\n\r\n" +
				"export const x = 1\r\n" +
				"--b1\r\n" +
				"Content-Disposition: form-data; name=\"index.mjs\"; filename=\"index.mjs\"\r\n" +
				"Content-Type: application/javascript+module\r\n\r\n" +
				"export default {}\n\r\n" +
				"--b1--\r\n"))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":10007,"message":"workers.api.error.script_not_found"}],"result":null}`))
		}
	})

	got, err := c.GetWorkerScriptContent(context.Background(), "acct1", "hello")
	if err != nil || got.MainModule != "index.mjs" || got.Content != "export default {}\n" {
		t.Errorf("GetWorkerScriptContent = (%+v, %v)", got, err)
	}

	_, err = c.GetWorkerScriptContent(context.Background(), "acct1", "missing")
	if !IsNotFound(err) {
		t.Errorf("expected not found, got %v", err)
	}
	var re *ResponseError
	if !errors.As(err, &re) || len(re.Errors) != 1 || re.Errors[0].Code != 10007 {
		t.Errorf("expected API error details, got %v", err)
	}
}
