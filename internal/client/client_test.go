package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newTestClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return New("test-token", WithBaseURL(srv.URL))
}

func TestCreateDNSRecord(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/zones/z1/dns_records" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("Authorization = %q", got)
		}
		body, _ := io.ReadAll(r.Body)
		var rec DNSRecord
		if err := json.Unmarshal(body, &rec); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		rec.ID = "r1"
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "errors": []any{}, "result": rec})
	})

	got, err := c.CreateDNSRecord(context.Background(), "z1", DNSRecord{Name: "www", Type: "A", Content: "192.0.2.1", TTL: 1})
	if err != nil {
		t.Fatalf("CreateDNSRecord: %v", err)
	}
	if got.ID != "r1" || got.Content != "192.0.2.1" {
		t.Errorf("unexpected result: %+v", got)
	}
}

func TestGetDNSRecordNotFound(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":81044,"message":"Record does not exist."}],"result":null}`))
	})

	_, err := c.GetDNSRecord(context.Background(), "z1", "missing")
	if !IsNotFound(err) {
		t.Fatalf("IsNotFound(%v) = false, want true", err)
	}
	if want := "cloudflare API returned HTTP 404: 81044: Record does not exist."; err.Error() != want {
		t.Errorf("err = %q, want %q", err.Error(), want)
	}
}

func TestErrorOnSuccessFalse(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":1004,"message":"DNS Validation Error"}],"result":null}`))
	})

	if _, err := c.CreateDNSRecord(context.Background(), "z1", DNSRecord{}); err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestErrorOnNonJSONBody(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("<html>bad gateway</html>"))
	})

	_, err := c.GetDNSRecord(context.Background(), "z1", "r1")
	if want := "cloudflare API returned HTTP 502"; err == nil || err.Error() != want {
		t.Errorf("err = %v, want %q", err, want)
	}
}
