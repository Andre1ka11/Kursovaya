// Package config reads and validates application settings.
package config

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"regexp"
	"strconv"
	"strings"
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
	Telegram          Telegram
	Database          Database
}

// Telegram keeps the token out of YAML and serialization.
type Telegram struct {
	Enabled        bool
	Token          string `json:"-" yaml:"-"`
	AllowedUserIDs []int64
}

type Database struct {
	Enabled        bool
	Host           string
	Port           uint16
	Name           string
	User           string
	Password       string `json:"-" yaml:"-"`
	ConnectTimeout time.Duration
	MaxConns       int32
}

type fileConfig struct {
	Log struct {
		Level  string `yaml:"level"`
		Format string `yaml:"format"`
	} `yaml:"log"`
	Timezone          string `yaml:"timezone"`
	ShutdownTimeout   string `yaml:"shutdown_timeout"`
	HeartbeatInterval string `yaml:"heartbeat_interval"`
	Telegram          struct {
		Enabled        bool    `yaml:"enabled"`
		AllowedUserIDs []int64 `yaml:"allowed_user_ids"`
	} `yaml:"telegram"`
	Database struct {
		Enabled        bool   `yaml:"enabled"`
		Host           string `yaml:"host"`
		Port           int    `yaml:"port"`
		Name           string `yaml:"name"`
		User           string `yaml:"user"`
		ConnectTimeout string `yaml:"connect_timeout"`
		MaxConns       int32  `yaml:"max_conns"`
	} `yaml:"database"`
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
	raw.Database.Host, raw.Database.Port = "127.0.0.1", 5432
	raw.Database.Name, raw.Database.User = "study_agent", "study_agent"
	raw.Database.ConnectTimeout, raw.Database.MaxConns = "10s", 4
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
		"AGENT_LOG_LEVEL":                &raw.Log.Level,
		"AGENT_LOG_FORMAT":               &raw.Log.Format,
		"AGENT_TIMEZONE":                 &raw.Timezone,
		"AGENT_SHUTDOWN_TIMEOUT":         &raw.ShutdownTimeout,
		"AGENT_HEARTBEAT_INTERVAL":       &raw.HeartbeatInterval,
		"AGENT_DATABASE_HOST":            &raw.Database.Host,
		"AGENT_DATABASE_NAME":            &raw.Database.Name,
		"AGENT_DATABASE_USER":            &raw.Database.User,
		"AGENT_DATABASE_CONNECT_TIMEOUT": &raw.Database.ConnectTimeout,
	}
	for name, target := range settings {
		if value, ok := lookup(name); ok {
			*target = value
		} else if value, ok := localEnv[name]; ok {
			*target = value
		}
	}
	// Secrets use the same precedence, without changing os.Environ.
	value := func(name string) (string, bool) {
		if v, ok := lookup(name); ok {
			return v, true
		}
		v, ok := localEnv[name]
		return v, ok
	}
	if enabled, ok := value("AGENT_DATABASE_ENABLED"); ok {
		raw.Database.Enabled, err = strconv.ParseBool(enabled)
		if err != nil {
			return Config{}, errors.New("AGENT_DATABASE_ENABLED must be true or false")
		}
	}
	if port, ok := value("AGENT_DATABASE_PORT"); ok {
		raw.Database.Port, err = strconv.Atoi(port)
		if err != nil {
			return Config{}, errors.New("AGENT_DATABASE_PORT must be an integer between 1 and 65535")
		}
	}
	if maxConns, ok := value("AGENT_DATABASE_MAX_CONNS"); ok {
		n, parseErr := strconv.ParseInt(maxConns, 10, 32)
		if parseErr != nil {
			return Config{}, errors.New("AGENT_DATABASE_MAX_CONNS must be an integer between 1 and 32")
		}
		raw.Database.MaxConns = int32(n)
	}
	if enabled, ok := value("AGENT_TELEGRAM_ENABLED"); ok {
		raw.Telegram.Enabled, err = strconv.ParseBool(enabled)
		if err != nil {
			return Config{}, errors.New("AGENT_TELEGRAM_ENABLED must be true or false")
		}
	}
	if ids, ok := value("AGENT_ALLOWED_USER_IDS"); ok {
		raw.Telegram.AllowedUserIDs = nil
		if strings.TrimSpace(ids) != "" {
			for _, item := range strings.Split(ids, ",") {
				id, parseErr := strconv.ParseInt(strings.TrimSpace(item), 10, 64)
				if parseErr != nil {
					return Config{}, errors.New("AGENT_ALLOWED_USER_IDS must contain positive numeric IDs separated by commas")
				}
				raw.Telegram.AllowedUserIDs = append(raw.Telegram.AllowedUserIDs, id)
			}
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
	cfg.Telegram.Enabled = raw.Telegram.Enabled
	cfg.Telegram.AllowedUserIDs = raw.Telegram.AllowedUserIDs
	if token, ok := value("AGENT_BOT_TOKEN"); ok {
		cfg.Telegram.Token = strings.TrimSpace(token)
	}
	if err := cfg.Telegram.Validate(); err != nil {
		return Config{}, err
	}
	if raw.Database.Port < 1 || raw.Database.Port > 65535 {
		return Config{}, errors.New("database.port must be between 1 and 65535")
	}
	cfg.Database = Database{Enabled: raw.Database.Enabled, Host: raw.Database.Host, Port: uint16(raw.Database.Port),
		Name: raw.Database.Name, User: raw.Database.User, MaxConns: raw.Database.MaxConns}
	cfg.Database.ConnectTimeout, err = positiveDuration("database.connect_timeout", raw.Database.ConnectTimeout)
	if err != nil {
		return Config{}, err
	}
	if password, ok := value("AGENT_DATABASE_PASSWORD"); ok {
		cfg.Database.Password = password
	}
	if err := cfg.Database.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (d Database) Validate() error {
	if strings.TrimSpace(d.Host) == "" || strings.TrimSpace(d.Name) == "" || strings.TrimSpace(d.User) == "" {
		return errors.New("database host, name and user must not be empty")
	}
	if d.Port == 0 || d.MaxConns < 1 || d.MaxConns > 32 || d.ConnectTimeout <= 0 {
		return errors.New("database requires a valid port, positive connect_timeout and max_conns between 1 and 32")
	}
	if d.Enabled && d.Password == "" {
		return errors.New("AGENT_DATABASE_PASSWORD is required when database is enabled")
	}
	return nil
}

var tokenPattern = regexp.MustCompile(`^[0-9]+:[A-Za-z0-9_-]+$`)

func (t Telegram) Validate() error {
	if !t.Enabled {
		return nil
	}
	if !tokenPattern.MatchString(t.Token) {
		return errors.New("AGENT_BOT_TOKEN must contain a bot token from BotFather")
	}
	if len(t.AllowedUserIDs) == 0 {
		return errors.New("telegram.allowed_user_ids or AGENT_ALLOWED_USER_IDS must contain at least one user ID")
	}
	seen := make(map[int64]bool)
	for _, id := range t.AllowedUserIDs {
		if id <= 0 || seen[id] {
			return errors.New("Telegram user IDs must be positive and unique")
		}
		seen[id] = true
	}
	return nil
}

func positiveDuration(name, value string) (time.Duration, error) {
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration, for example 10s", name)
	}
	return duration, nil
}
