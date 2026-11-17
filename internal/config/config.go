// Package config reads and validates application settings.
package config

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"
	_ "time/tzdata" // Keep IANA time zones available in a standalone Windows build.

	"github.com/joho/godotenv"
	"go.yaml.in/yaml/v3"
)

type Config struct {
	LogLevel          slog.Level
	LogFormat         string
	Timezone          *time.Location
	ShutdownTimeout   time.Duration
	HeartbeatInterval time.Duration
}

type fileConfig struct {
	Log struct {
		Level  string `yaml:"level"`
		Format string `yaml:"format"`
	} `yaml:"log"`
	Timezone          string `yaml:"timezone"`
	ShutdownTimeout   string `yaml:"shutdown_timeout"`
	HeartbeatInterval string `yaml:"heartbeat_interval"`
}

// Load applies settings in this order: YAML < optional .env < process environment.
// It does not modify the process environment and never logs secret values.
func Load(configPath, envPath string) (Config, error) {
	return load(configPath, envPath, os.LookupEnv)
}

func load(configPath, envPath string, lookup func(string) (string, bool)) (Config, error) {
	file, err := os.Open(configPath)
	if err != nil {
		return Config{}, fmt.Errorf("open config: %w", err)
	}
	defer file.Close()

	var raw fileConfig
	decoder := yaml.NewDecoder(file)
	decoder.KnownFields(true)
	if err := decoder.Decode(&raw); err != nil {
		return Config{}, fmt.Errorf("decode config: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return Config{}, errors.New("config must contain exactly one YAML document")
	}

	localEnv := map[string]string{}
	if envPath != "" {
		localEnv, err = godotenv.Read(envPath)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			// Do not include parser errors: they may contain a credential's value.
			return Config{}, fmt.Errorf("cannot read .env file %q; check permissions and KEY=value syntax", envPath)
		}
	}

	settings := map[string]*string{
		"AGENT_LOG_LEVEL":          &raw.Log.Level,
		"AGENT_LOG_FORMAT":         &raw.Log.Format,
		"AGENT_TIMEZONE":           &raw.Timezone,
		"AGENT_SHUTDOWN_TIMEOUT":   &raw.ShutdownTimeout,
		"AGENT_HEARTBEAT_INTERVAL": &raw.HeartbeatInterval,
	}
	for name, target := range settings {
		if value, ok := lookup(name); ok {
			*target = value
		} else if value, ok := localEnv[name]; ok {
			*target = value
		}
	}

	var cfg Config
	switch raw.Log.Level {
	case "debug":
		cfg.LogLevel = slog.LevelDebug
	case "info":
		cfg.LogLevel = slog.LevelInfo
	case "warn":
		cfg.LogLevel = slog.LevelWarn
	case "error":
		cfg.LogLevel = slog.LevelError
	default:
		return Config{}, errors.New("log.level must be debug, info, warn or error")
	}
	if raw.Log.Format != "text" && raw.Log.Format != "json" {
		return Config{}, errors.New("log.format must be text or json")
	}
	cfg.LogFormat = raw.Log.Format
	if raw.Timezone == "" {
		return Config{}, errors.New("timezone must not be empty")
	}
	cfg.Timezone, err = time.LoadLocation(raw.Timezone)
	if err != nil {
		return Config{}, errors.New("timezone must be a valid IANA time zone, for example Europe/Moscow")
	}
	cfg.ShutdownTimeout, err = positiveDuration("shutdown_timeout", raw.ShutdownTimeout)
	if err != nil {
		return Config{}, err
	}
	cfg.HeartbeatInterval, err = positiveDuration("heartbeat_interval", raw.HeartbeatInterval)
	if err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func positiveDuration(name, value string) (time.Duration, error) {
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration, for example 10s", name)
	}
	return duration, nil
}
