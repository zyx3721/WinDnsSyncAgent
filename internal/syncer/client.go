package syncer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"windnssyncagent/internal/dns"
)

type client struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

type responseEnvelope[T any] struct {
	Success bool `json:"success"`
	Data    T    `json:"data"`
	Error   *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func newClient(baseURL, apiKey string, timeout time.Duration) client {
	if timeout <= 0 {
		timeout = 90 * time.Second
	}
	return client{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		http:    &http.Client{Timeout: timeout},
	}
}

func (c client) health(ctx context.Context) error {
	var data map[string]any
	return c.do(ctx, http.MethodGet, "/health", nil, &data)
}

func (c client) listZones(ctx context.Context) ([]dns.Zone, error) {
	var zones []dns.Zone
	err := c.do(ctx, http.MethodGet, "/dns/zones", nil, &zones)
	return zones, err
}

func (c client) createZone(ctx context.Context, zone dns.Zone) error {
	var data map[string]any
	return c.do(ctx, http.MethodPost, "/dns/zones", zone, &data)
}

func (c client) deleteZone(ctx context.Context, zone string) error {
	var data map[string]any
	return c.do(ctx, http.MethodDelete, "/dns/zones/"+url.PathEscape(zone), nil, &data)
}

func (c client) listRecords(ctx context.Context, zone string) ([]dns.Record, error) {
	var records []dns.Record
	if err := c.do(ctx, http.MethodPost, "/dns/records/query", map[string]string{"zone": zone}, &records); err != nil {
		if !isHTTPStatus(err, http.StatusNotFound) {
			return nil, err
		}
	} else {
		return records, nil
	}
	path := "/dns/zones/" + url.PathEscape(zone) + "/records"
	err := c.do(ctx, http.MethodGet, path, nil, &records)
	return records, err
}

func (c client) createRecord(ctx context.Context, zone string, record dns.Record) error {
	var data map[string]any
	path := "/dns/zones/" + url.PathEscape(zone) + "/records"
	return c.do(ctx, http.MethodPost, path, record, &data)
}

func (c client) deleteRecord(ctx context.Context, zone string, record dns.Record) error {
	var data map[string]any
	path := "/dns/zones/" + url.PathEscape(zone) + "/records/" + url.PathEscape(record.Type) + "/" + url.PathEscape(record.Name) + "?value=" + url.QueryEscape(record.Value)
	return c.do(ctx, http.MethodDelete, path, nil, &data)
}

func (c client) applyRecordBatch(ctx context.Context, zone string, batch dns.RecordBatch) error {
	var data map[string]any
	if err := c.do(ctx, http.MethodPost, "/dns/records/batch", map[string]any{"zone": zone, "batch": batch}, &data); err != nil {
		if !isHTTPStatus(err, http.StatusNotFound) {
			return err
		}
	} else {
		return nil
	}
	path := "/dns/zones/" + url.PathEscape(zone) + "/records/batch"
	return c.do(ctx, http.MethodPost, path, batch, &data)
}

func (c client) do(ctx context.Context, method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.apiKey != "" {
		req.Header.Set("X-API-Key", c.apiKey)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	var env responseEnvelope[json.RawMessage]
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("%s %s returned non-json response: %s", method, path, string(raw))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || !env.Success {
		if env.Error != nil {
			return httpStatusError{method: method, path: path, statusCode: resp.StatusCode, message: env.Error.Message}
		}
		return httpStatusError{method: method, path: path, statusCode: resp.StatusCode}
	}
	if out != nil && len(env.Data) > 0 {
		if err := json.Unmarshal(env.Data, out); err != nil {
			return fmt.Errorf("parse response data: %w", err)
		}
	}
	return nil
}

type httpStatusError struct {
	method     string
	path       string
	statusCode int
	message    string
}

func (e httpStatusError) Error() string {
	if strings.TrimSpace(e.message) != "" {
		return fmt.Sprintf("%s %s failed: %s", e.method, e.path, e.message)
	}
	return fmt.Sprintf("%s %s failed with status %d", e.method, e.path, e.statusCode)
}

func isHTTPStatus(err error, statusCode int) bool {
	var statusErr httpStatusError
	return errors.As(err, &statusErr) && statusErr.statusCode == statusCode
}
