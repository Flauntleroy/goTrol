package config

import (
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Database DatabaseConfig `yaml:"database"`
	Watcher  WatcherConfig  `yaml:"watcher"`
	API      APIConfig      `yaml:"api"`
	Report   ReportConfig   `yaml:"report"`
}

type DatabaseConfig struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	User     string `yaml:"user"`
	Password string `yaml:"password"`
	Name     string `yaml:"name"`
}

type WatcherConfig struct {
	PollInterval string `yaml:"poll_interval"`
}

type APIConfig struct {
	Enabled bool `yaml:"enabled"`
	Port    int  `yaml:"port"`
}

type ReportConfig struct {
	DBPath string `yaml:"db_path"`
}

// BPJSCredentials from mlite_settings table
type BPJSCredentials struct {
	ConsID     string
	SecretKey  string
	AntrianURL string
	UserKey    string
	KdPjBPJS   string
}

func (w *WatcherConfig) GetPollDuration() time.Duration {
	d, err := time.ParseDuration(w.PollInterval)
	if err != nil {
		return 5 * time.Second
	}
	return d
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}
