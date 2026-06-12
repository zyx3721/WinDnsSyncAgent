package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"windnssyncagent/internal/config"
	"windnssyncagent/internal/dns"
)

type Server struct {
	cfg      config.Agent
	provider dns.Provider
	logger   *log.Logger
}

type envelope struct {
	Success bool      `json:"success"`
	Data    any       `json:"data,omitempty"`
	Error   *apiError `json:"error,omitempty"`
	Request string    `json:"requestId"`
}

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func NewServer(cfg config.Agent, provider dns.Provider) *Server {
	return &Server{cfg: cfg, provider: provider, logger: newLogger(cfg.LogPath)}
}

func (s *Server) ListenAndServe(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/dns/zones", s.handleZones)
	mux.HandleFunc("/dns/records/query", s.handleRecordQuery)
	mux.HandleFunc("/dns/records/batch", s.handleRecordBatchByBody)
	mux.HandleFunc("/dns/zones/", s.handleZoneRoutes)

	addr := fmt.Sprintf(":%d", s.cfg.Port)
	server := &http.Server{
		Addr:              addr,
		Handler:           s.withMiddleware(mux),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		s.logger.Printf("agent started on %s://+:%d", s.cfg.Scheme, s.cfg.Port)
		if s.cfg.Scheme == "https" {
			errCh <- errors.New("https listener is not implemented yet; use http or put the agent behind a reverse proxy")
			return
		}
		errCh <- server.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func (s *Server) withMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		requestID := newRequestID()
		w.Header().Set("X-Request-ID", requestID)

		if r.URL.Path != "/health" && !s.cfg.AllowAnonymous && r.Header.Get("X-API-Key") != s.cfg.APIKey {
			writeError(w, http.StatusUnauthorized, requestID, "UNAUTHORIZED", "invalid api key")
			return
		}

		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey{}, requestID)))
		s.logger.Printf("%s %s requestId=%s elapsedMs=%d", r.Method, r.URL.Path, requestID, time.Since(started).Milliseconds())
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, requestID(r), "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, envelope{Success: true, Request: requestID(r), Data: map[string]any{
		"status":                   "ok",
		"time":                     time.Now().Format(time.RFC3339Nano),
		"powerShellTimeoutSeconds": s.cfg.PowerShellTimeoutSeconds,
	}})
}

func (s *Server) handleZones(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		zones, err := s.provider.ListZones(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, requestID(r), "DNS_ERROR", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, envelope{Success: true, Request: requestID(r), Data: zones})
	case http.MethodPost:
		var zone dns.Zone
		if err := json.NewDecoder(r.Body).Decode(&zone); err != nil {
			writeError(w, http.StatusBadRequest, requestID(r), "BAD_REQUEST", err.Error())
			return
		}
		if err := s.provider.CreateZone(r.Context(), zone); err != nil {
			writeError(w, http.StatusInternalServerError, requestID(r), "DNS_ERROR", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, envelope{Success: true, Request: requestID(r), Data: map[string]bool{"created": true}})
	default:
		writeError(w, http.StatusMethodNotAllowed, requestID(r), "METHOD_NOT_ALLOWED", "method not allowed")
	}
}

func (s *Server) handleRecordQuery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, requestID(r), "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}
	var body struct {
		Zone string `json:"zone"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, requestID(r), "BAD_REQUEST", err.Error())
		return
	}
	zone := strings.TrimSpace(body.Zone)
	if zone == "" {
		writeError(w, http.StatusBadRequest, requestID(r), "BAD_REQUEST", "zone is required")
		return
	}
	records, err := s.provider.ListRecords(r.Context(), zone)
	if err != nil {
		writeError(w, http.StatusInternalServerError, requestID(r), "DNS_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, envelope{Success: true, Request: requestID(r), Data: records})
}

func (s *Server) handleRecordBatchByBody(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, requestID(r), "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}
	var body struct {
		Zone  string          `json:"zone"`
		Batch dns.RecordBatch `json:"batch"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, requestID(r), "BAD_REQUEST", err.Error())
		return
	}
	zone := strings.TrimSpace(body.Zone)
	if zone == "" {
		writeError(w, http.StatusBadRequest, requestID(r), "BAD_REQUEST", "zone is required")
		return
	}
	if err := s.provider.ApplyRecordBatch(r.Context(), zone, body.Batch); err != nil {
		writeError(w, http.StatusInternalServerError, requestID(r), "DNS_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, envelope{Success: true, Request: requestID(r), Data: map[string]int{
		"added":   len(body.Batch.Add),
		"deleted": len(body.Batch.Delete),
		"updated": len(body.Batch.Update),
	}})
}

func (s *Server) handleZoneRoutes(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/dns/zones/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		writeError(w, http.StatusNotFound, requestID(r), "NOT_FOUND", "route not found")
		return
	}
	zone := parts[0]

	if len(parts) == 1 && r.Method == http.MethodDelete {
		if err := s.provider.DeleteZone(r.Context(), zone); err != nil {
			writeError(w, http.StatusInternalServerError, requestID(r), "DNS_ERROR", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, envelope{Success: true, Request: requestID(r), Data: map[string]bool{"deleted": true}})
		return
	}

	if len(parts) == 2 && parts[1] == "records" {
		s.handleRecords(w, r, zone)
		return
	}

	if len(parts) == 3 && parts[1] == "records" && parts[2] == "batch" {
		s.handleRecordBatch(w, r, zone)
		return
	}

	if len(parts) == 4 && parts[1] == "records" && r.Method == http.MethodDelete {
		record := dns.Record{ZoneID: zone, Type: parts[2], Name: parts[3], Value: r.URL.Query().Get("value")}
		if err := s.provider.DeleteRecord(r.Context(), zone, record); err != nil {
			writeError(w, http.StatusInternalServerError, requestID(r), "DNS_ERROR", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, envelope{Success: true, Request: requestID(r), Data: map[string]bool{"deleted": true}})
		return
	}

	writeError(w, http.StatusNotFound, requestID(r), "NOT_FOUND", "route not found")
}

func (s *Server) handleRecordBatch(w http.ResponseWriter, r *http.Request, zone string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, requestID(r), "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}

	var batch dns.RecordBatch
	if err := json.NewDecoder(r.Body).Decode(&batch); err != nil {
		writeError(w, http.StatusBadRequest, requestID(r), "BAD_REQUEST", err.Error())
		return
	}
	if err := s.provider.ApplyRecordBatch(r.Context(), zone, batch); err != nil {
		writeError(w, http.StatusInternalServerError, requestID(r), "DNS_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, envelope{Success: true, Request: requestID(r), Data: map[string]int{
		"added":   len(batch.Add),
		"deleted": len(batch.Delete),
		"updated": len(batch.Update),
	}})
}

func (s *Server) handleRecords(w http.ResponseWriter, r *http.Request, zone string) {
	switch r.Method {
	case http.MethodGet:
		records, err := s.provider.ListRecords(r.Context(), zone)
		if err != nil {
			writeError(w, http.StatusInternalServerError, requestID(r), "DNS_ERROR", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, envelope{Success: true, Request: requestID(r), Data: records})
	case http.MethodPost:
		var record dns.Record
		if err := json.NewDecoder(r.Body).Decode(&record); err != nil {
			writeError(w, http.StatusBadRequest, requestID(r), "BAD_REQUEST", err.Error())
			return
		}
		if err := s.provider.CreateRecord(r.Context(), zone, record); err != nil {
			writeError(w, http.StatusInternalServerError, requestID(r), "DNS_ERROR", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, envelope{Success: true, Request: requestID(r), Data: map[string]bool{"created": true}})
	default:
		writeError(w, http.StatusMethodNotAllowed, requestID(r), "METHOD_NOT_ALLOWED", "method not allowed")
	}
}

func writeJSON(w http.ResponseWriter, status int, body envelope) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, requestID, code, message string) {
	writeJSON(w, status, envelope{Success: false, Request: requestID, Error: &apiError{Code: code, Message: message}})
}

type requestIDKey struct{}

func requestID(r *http.Request) string {
	if v, ok := r.Context().Value(requestIDKey{}).(string); ok {
		return v
	}
	return ""
}

func newRequestID() string {
	return fmt.Sprintf("%d", time.Now().UnixNano())
}

func newLogger(path string) *log.Logger {
	if strings.TrimSpace(path) == "" {
		return log.New(os.Stdout, "", log.LstdFlags)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return log.New(os.Stdout, "", log.LstdFlags)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return log.New(os.Stdout, "", log.LstdFlags)
	}
	return log.New(file, "", log.LstdFlags)
}
