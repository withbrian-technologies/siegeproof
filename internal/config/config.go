package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	DefaultIntensity   = "low"
	DefaultSandbox     = "none"
	DefaultMaxRequests = 100
	DefaultTimeout     = "30s"
	DefaultReportPath  = "reports/report.json"
)

type Config struct {
	AcknowledgeAuthorization bool     `yaml:"acknowledge_authorization" json:"acknowledge_authorization"`
	Target                   Target   `yaml:"target" json:"target"`
	Intensity                string   `yaml:"intensity" json:"intensity"`
	AllowHosts               []string `yaml:"allow_hosts" json:"allow_hosts"`
	Sandbox                  string   `yaml:"sandbox" json:"sandbox"`
	Budgets                  Budgets  `yaml:"budgets" json:"budgets"`
	ReportPath               string   `yaml:"report_path" json:"report_path"`
}

type Target struct {
	Transport string   `yaml:"transport" json:"transport"`
	Command   []string `yaml:"command" json:"command"`
	URL       string   `yaml:"url" json:"url"`
}

type Budgets struct {
	MaxRequests int    `yaml:"max_requests" json:"max_requests"`
	Timeout     string `yaml:"timeout" json:"timeout"`
}

func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	var cfg Config
	decoder := yaml.NewDecoder(strings.NewReader(string(data)))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}
	ApplyDefaults(&cfg)
	if err := Validate(cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func ApplyDefaults(cfg *Config) {
	if cfg.AllowHosts == nil {
		cfg.AllowHosts = []string{}
	}
	if cfg.Intensity == "" {
		cfg.Intensity = DefaultIntensity
	}
	if cfg.Sandbox == "" {
		cfg.Sandbox = DefaultSandbox
	}
	if cfg.Budgets.MaxRequests == 0 {
		cfg.Budgets.MaxRequests = DefaultMaxRequests
	}
	if cfg.Budgets.Timeout == "" {
		cfg.Budgets.Timeout = DefaultTimeout
	}
	if cfg.ReportPath == "" {
		cfg.ReportPath = DefaultReportPath
	}
}

func Validate(cfg Config) error {
	if !cfg.AcknowledgeAuthorization {
		return errors.New("acknowledge_authorization must be true: only assess targets you own or are explicitly authorized to test")
	}
	switch cfg.Target.Transport {
	case "stdio":
		if len(cfg.Target.Command) == 0 || strings.TrimSpace(cfg.Target.Command[0]) == "" {
			return errors.New("target.command must contain a program for stdio transport")
		}
		if cfg.Target.URL != "" {
			return errors.New("target.url is only valid for http or sse transport")
		}
	case "http", "sse":
		if strings.TrimSpace(cfg.Target.URL) == "" {
			return fmt.Errorf("target.url is required for %s transport", cfg.Target.Transport)
		}
		u, err := url.Parse(cfg.Target.URL)
		if err != nil || u.Scheme != "http" && u.Scheme != "https" || u.Hostname() == "" {
			return errors.New("target.url must be an absolute http or https URL")
		}
		if len(cfg.Target.Command) != 0 {
			return errors.New("target.command is only valid for stdio transport")
		}
	default:
		return fmt.Errorf("target.transport %q is unsupported; use stdio, http, or sse", cfg.Target.Transport)
	}
	switch cfg.Intensity {
	case "low", "medium", "high":
	default:
		return fmt.Errorf("intensity %q is unsupported; use low, medium, or high", cfg.Intensity)
	}
	switch cfg.Sandbox {
	case "none", "bwrap", "docker":
	default:
		return fmt.Errorf("sandbox %q is unsupported; use none, bwrap, or docker", cfg.Sandbox)
	}
	if cfg.Budgets.MaxRequests <= 0 || cfg.Budgets.MaxRequests > 100000 {
		return errors.New("budgets.max_requests must be between 1 and 100000")
	}
	timeout, err := time.ParseDuration(cfg.Budgets.Timeout)
	if err != nil || timeout <= 0 || timeout > 24*time.Hour {
		return errors.New("budgets.timeout must be a positive duration no longer than 24h")
	}
	if filepath.IsAbs(cfg.ReportPath) || strings.TrimSpace(cfg.ReportPath) == "" {
		return errors.New("report_path must be a non-empty relative path")
	}
	for _, host := range cfg.AllowHosts {
		if strings.TrimSpace(host) == "" {
			return errors.New("allow_hosts cannot contain empty values")
		}
		if net.ParseIP(host) == nil && strings.ContainsAny(host, " /\\:") {
			return fmt.Errorf("allow_hosts entry %q is not a hostname or IP address", host)
		}
	}
	if cfg.Target.Transport != "stdio" && len(cfg.AllowHosts) == 0 {
		return errors.New("allow_hosts must list at least one host for network transports")
	}
	return nil
}
