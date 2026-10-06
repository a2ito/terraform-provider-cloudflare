package client

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
)

type DNSRecord struct {
	ID      string `json:"id,omitempty"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	Content string `json:"content"`
	TTL     int64  `json:"ttl"`
	Proxied bool   `json:"proxied"`
	Comment string `json:"comment"`
}

func dnsRecordsPath(zoneID string) string {
	return fmt.Sprintf("/zones/%s/dns_records", url.PathEscape(zoneID))
}

func dnsRecordPath(zoneID, recordID string) string {
	return dnsRecordsPath(zoneID) + "/" + url.PathEscape(recordID)
}

func (c *Client) CreateDNSRecord(ctx context.Context, zoneID string, rec DNSRecord) (DNSRecord, error) {
	return do[DNSRecord](ctx, c, http.MethodPost, dnsRecordsPath(zoneID), rec)
}

func (c *Client) GetDNSRecord(ctx context.Context, zoneID, recordID string) (DNSRecord, error) {
	return do[DNSRecord](ctx, c, http.MethodGet, dnsRecordPath(zoneID, recordID), nil)
}

// UpdateDNSRecord はレコードを PUT で丸ごと置き換える。
func (c *Client) UpdateDNSRecord(ctx context.Context, zoneID, recordID string, rec DNSRecord) (DNSRecord, error) {
	return do[DNSRecord](ctx, c, http.MethodPut, dnsRecordPath(zoneID, recordID), rec)
}

func (c *Client) DeleteDNSRecord(ctx context.Context, zoneID, recordID string) error {
	_, err := do[struct {
		ID string `json:"id"`
	}](ctx, c, http.MethodDelete, dnsRecordPath(zoneID, recordID), nil)
	return err
}
