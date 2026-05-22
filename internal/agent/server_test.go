package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"windnssyncagent/internal/config"
	"windnssyncagent/internal/dns"
)

type fakeProvider struct{}

func (fakeProvider) ListZones(context.Context) ([]dns.Zone, error) { return []dns.Zone{}, nil }
func (fakeProvider) CreateZone(context.Context, dns.Zone) error    { return nil }
func (fakeProvider) DeleteZone(context.Context, string) error      { return nil }
func (fakeProvider) ListRecords(context.Context, string) ([]dns.Record, error) {
	return []dns.Record{}, nil
}
func (fakeProvider) CreateRecord(context.Context, string, dns.Record) error { return nil }
func (fakeProvider) DeleteRecord(context.Context, string, dns.Record) error { return nil }
func (fakeProvider) ApplyRecordBatch(context.Context, string, dns.RecordBatch) error {
	return nil
}

func TestHealthBypassesAPIKey(t *testing.T) {
	server := NewServer(config.Agent{AllowAnonymous: false, APIKey: "secret"}, fakeProvider{})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/health", nil)

	server.withMiddleware(http.HandlerFunc(server.handleHealth)).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected health without api key to return 200, got %d", recorder.Code)
	}
}

func TestProtectedRouteRequiresAPIKey(t *testing.T) {
	server := NewServer(config.Agent{AllowAnonymous: false, APIKey: "secret"}, fakeProvider{})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/dns/zones", nil)

	server.withMiddleware(http.HandlerFunc(server.handleZones)).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("expected protected route without api key to return 401, got %d", recorder.Code)
	}
}
