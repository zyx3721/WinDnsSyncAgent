package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

type Agent struct {
	Scheme         string `json:"scheme"`
	Port           int    `json:"port"`
	AllowAnonymous bool   `json:"allowAnonymous"`
	APIKey         string `json:"apiKey"`
	LogPath        string `json:"logPath"`
}

type Sync struct {
	SourceAgent string `json:"sourceAgent"`
	TargetAgent string `json:"targetAgent"`

	APIKey       string `json:"apiKey"`
	SourceAPIKey string `json:"sourceApiKey"`
	TargetAPIKey string `json:"targetApiKey"`

	IncludeZones    []string        `json:"includeZones"`
	ExcludeZones    []string        `json:"excludeZones"`
	Zones           []string        `json:"zones,omitempty"`
	ZoneConcurrency int             `json:"zoneConcurrency"`
	SyncMode        string          `json:"syncMode"`
	DryRun          bool            `json:"dryRun"`
	CreatePTR       bool            `json:"createPtrRecords"`
	EnableRewrite   bool            `json:"enableRewriteRecords"`
	RewriteRecords  []RewriteRecord `json:"rewriteRecords"`
}

type RewriteRecord struct {
	Zone     string `json:"zone"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	OldIP    string `json:"oldIp"`
	TargetIP string `json:"targetIp"`
	TTL      int    `json:"ttl"`
}

func LoadAgent(path string) (Agent, error) {
	var cfg Agent
	if err := loadJSON(path, &cfg); err != nil {
		return cfg, err
	}

	cfg.Scheme = strings.ToLower(strings.TrimSpace(cfg.Scheme))
	if cfg.Scheme == "" {
		cfg.Scheme = "http"
	}
	if cfg.Scheme != "http" && cfg.Scheme != "https" {
		return cfg, fmt.Errorf("scheme must be http or https")
	}
	if cfg.Port <= 0 || cfg.Port > 65535 {
		return cfg, fmt.Errorf("port must be between 1 and 65535")
	}
	if !cfg.AllowAnonymous && strings.TrimSpace(cfg.APIKey) == "" {
		return cfg, fmt.Errorf("apiKey is required when allowAnonymous is false")
	}
	return cfg, nil
}

func LoadSync(path string) (Sync, error) {
	var cfg Sync
	if err := loadJSON(path, &cfg); err != nil {
		return cfg, err
	}

	cfg.SourceAgent = strings.TrimRight(strings.TrimSpace(cfg.SourceAgent), "/")
	cfg.TargetAgent = strings.TrimRight(strings.TrimSpace(cfg.TargetAgent), "/")
	if cfg.SourceAgent == "" {
		return cfg, fmt.Errorf("sourceAgent is required")
	}
	if cfg.TargetAgent == "" {
		return cfg, fmt.Errorf("targetAgent is required")
	}

	cfg.SyncMode = strings.ToLower(strings.TrimSpace(cfg.SyncMode))
	if cfg.SyncMode == "" {
		cfg.SyncMode = "mirror"
	}
	if cfg.SyncMode != "mirror" && cfg.SyncMode != "addonly" {
		return cfg, fmt.Errorf("syncMode must be mirror or addOnly")
	}
	if cfg.ZoneConcurrency <= 0 {
		cfg.ZoneConcurrency = 2
	}
	if cfg.ZoneConcurrency > 16 {
		return cfg, fmt.Errorf("zoneConcurrency must be between 1 and 16")
	}
	if len(cfg.IncludeZones) == 0 && len(cfg.Zones) > 0 {
		cfg.IncludeZones = cfg.Zones
	}
	cfg.Zones = cfg.IncludeZones

	if cfg.SourceAPIKey == "" {
		cfg.SourceAPIKey = cfg.APIKey
	}
	if cfg.TargetAPIKey == "" {
		cfg.TargetAPIKey = cfg.APIKey
	}

	for i := range cfg.RewriteRecords {
		cfg.RewriteRecords[i].Type = strings.ToUpper(strings.TrimSpace(cfg.RewriteRecords[i].Type))
		if cfg.RewriteRecords[i].Type == "" {
			cfg.RewriteRecords[i].Type = "A"
		}
	}

	return cfg, nil
}

func loadJSON(path string, dst any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	return nil
}
