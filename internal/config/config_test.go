package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadSyncDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sync.json")
	content := `{
  "sourceAgent": "http://source:8443/",
  "targetAgent": "http://target:8443/",
  "apiKey": "secret",
  "dryRun": true,
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
