package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadAgentDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.json")
	content := `{
  "scheme": "http",
  "port": 8443,
  "allowAnonymous": true
}`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadAgent(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PowerShellTimeoutSeconds != 180 {
		t.Fatalf("expected powerShellTimeoutSeconds default 180, got %d", cfg.PowerShellTimeoutSeconds)
	}
}

func TestLoadAgentPowerShellTimeout(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.json")
	content := `{
  "scheme": "http",
  "port": 8443,
  "allowAnonymous": true,
  "powerShellTimeoutSeconds": 300
}`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadAgent(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PowerShellTimeoutSeconds != 300 {
		t.Fatalf("expected powerShellTimeoutSeconds 300, got %d", cfg.PowerShellTimeoutSeconds)
	}
}

func TestLoadAgentRejectsInvalidPowerShellTimeout(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.json")
	content := `{
  "scheme": "http",
  "port": 8443,
  "allowAnonymous": true,
  "powerShellTimeoutSeconds": 3601
}`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadAgent(path); err == nil {
		t.Fatal("expected invalid powerShellTimeoutSeconds to fail")
	}
}

func TestLoadSyncDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sync.json")
	content := `{
  "sourceAgent": "http://source:8443/",
  "targetAgent": "http://target:8443/",
  "apiKey": "secret",
  "dryRun": true,
  "excludeRecords": [{"zone":"example.com.","name":"ns.other.com","type":"a","value":"10.0.0.1"}],
  "rewriteRecords": [{"zone":"example.com","name":"www","targetIp":"10.0.0.1"}]
}`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadSync(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SourceAgent != "http://source:8443" || cfg.TargetAgent != "http://target:8443" {
		t.Fatalf("unexpected agent urls: %#v", cfg)
	}
	if cfg.SyncMode != "mirror" {
		t.Fatalf("expected mirror default, got %s", cfg.SyncMode)
	}
	if cfg.SourceAPIKey != "secret" || cfg.TargetAPIKey != "secret" {
		t.Fatalf("expected api key fallback, got %#v", cfg)
	}
	if cfg.RewriteRecords[0].Type != "A" {
		t.Fatalf("expected rewrite type A default, got %s", cfg.RewriteRecords[0].Type)
	}
	if cfg.EnableRewrite {
		t.Fatal("expected enableRewriteRecords default to false")
	}
	if cfg.CreatePTR {
		t.Fatal("expected createPtrRecords default to false")
	}
	if cfg.ZoneConcurrency != 2 {
		t.Fatalf("expected zoneConcurrency default 2, got %d", cfg.ZoneConcurrency)
	}
	if cfg.RecordBatchSize != 50 {
		t.Fatalf("expected recordBatchSize default 50, got %d", cfg.RecordBatchSize)
	}
	if cfg.RequestTimeoutSeconds != 90 {
		t.Fatalf("expected requestTimeoutSeconds default 90, got %d", cfg.RequestTimeoutSeconds)
	}
	if len(cfg.ExcludeRecords) != 1 || cfg.ExcludeRecords[0].Zone != "example.com" || cfg.ExcludeRecords[0].Type != "A" {
		t.Fatalf("unexpected excludeRecords normalization: %#v", cfg.ExcludeRecords)
	}
}

func TestLoadSyncBatchAndTimeoutSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sync.json")
	content := `{
  "sourceAgent": "http://source:8443/",
  "targetAgent": "http://target:8443/",
  "recordBatchSize": 10,
  "requestTimeoutSeconds": 300
}`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadSync(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RecordBatchSize != 10 {
		t.Fatalf("expected recordBatchSize 10, got %d", cfg.RecordBatchSize)
	}
	if cfg.RequestTimeoutSeconds != 300 {
		t.Fatalf("expected requestTimeoutSeconds 300, got %d", cfg.RequestTimeoutSeconds)
	}
}

func TestLoadSyncIncludeExcludeZones(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sync.json")
	content := `{
  "sourceAgent": "http://source:8443/",
  "targetAgent": "http://target:8443/",
  "includeZones": ["cursor.com"],
  "excludeZones": ["test.cursor.com"]
}`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadSync(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.IncludeZones) != 1 || cfg.IncludeZones[0] != "cursor.com" {
		t.Fatalf("unexpected includeZones: %#v", cfg.IncludeZones)
	}
	if len(cfg.ExcludeZones) != 1 || cfg.ExcludeZones[0] != "test.cursor.com" {
		t.Fatalf("unexpected excludeZones: %#v", cfg.ExcludeZones)
	}
}

func TestLoadSyncLegacyZonesMapsToIncludeZones(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sync.json")
	content := `{
  "sourceAgent": "http://source:8443/",
  "targetAgent": "http://target:8443/",
  "zones": ["legacy.example.com"]
}`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadSync(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.IncludeZones) != 1 || cfg.IncludeZones[0] != "legacy.example.com" {
		t.Fatalf("expected legacy zones to map to includeZones, got %#v", cfg.IncludeZones)
	}
}
