package config

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const validYAML = `log:
  level: info
  format: text
timezone: Europe/Moscow
shutdown_timeout: 10s
heartbeat_interval: 30s
`

func TestLoadWithoutEnvFile(t *testing.T) {
	path := writeFile(t, "agent.yaml", validYAML)
	cfg, err := load(path, filepath.Join(t.TempDir(), "missing.env"), environment(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LogLevel != slog.LevelInfo || cfg.LogFormat != "text" ||
		cfg.Timezone.String() != "Europe/Moscow" || cfg.ShutdownTimeout != 10*time.Second ||
		cfg.HeartbeatInterval != 30*time.Second {
		t.Fatalf("unexpected settings: %+v", cfg)
	}
}

func TestLoadEnvironmentPrecedence(t *testing.T) {
	path := writeFile(t, "agent.yaml", validYAML)
	envPath := writeFile(t, ".env", "AGENT_LOG_LEVEL=debug\nAGENT_LOG_FORMAT=json\nAGENT_TIMEZONE=UTC\nAGENT_SHUTDOWN_TIMEOUT=20s\n")
	cfg, err := load(path, envPath, environment(map[string]string{
		"AGENT_LOG_LEVEL":          "warn",
		"AGENT_HEARTBEAT_INTERVAL": "2s",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LogLevel != slog.LevelWarn || cfg.LogFormat != "json" ||
		cfg.Timezone.String() != "UTC" || cfg.ShutdownTimeout != 20*time.Second ||
		cfg.HeartbeatInterval != 2*time.Second {
		t.Fatalf("environment precedence is incorrect: %+v", cfg)
	}
}

func TestLoadRejectsInvalidConfiguration(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		want string
	}{
		{"unknown field", validYAML + "typo: true\n", "typo"},
		{"duplicate key", validYAML + "timezone: UTC\n", "timezone"},
		{"multiple documents", validYAML + "---\nextra: true\n", "exactly one"},
		{"bad YAML", "log: [\n", "decode config"},
		{"bad level", strings.Replace(validYAML, "level: info", "level: verbose", 1), "log.level"},
		{"bad format", strings.Replace(validYAML, "format: text", "format: xml", 1), "log.format"},
		{"bad timezone", strings.Replace(validYAML, "Europe/Moscow", "Not/AZone", 1), "timezone"},
		{"empty timezone", strings.Replace(validYAML, "Europe/Moscow", `""`, 1), "timezone"},
		{"zero timeout", strings.Replace(validYAML, "10s", "0s", 1), "shutdown_timeout"},
		{"negative interval", strings.Replace(validYAML, "30s", "-1s", 1), "heartbeat_interval"},
		{"bad duration", strings.Replace(validYAML, "10s", "tomorrow", 1), "shutdown_timeout"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := load(writeFile(t, "agent.yaml", tc.yaml), "", environment(nil))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestLoadRejectsEmptyEnvironmentOverride(t *testing.T) {
	_, err := load(writeFile(t, "agent.yaml", validYAML), "", environment(map[string]string{
		"AGENT_LOG_LEVEL": "",
	}))
	if err == nil || !strings.Contains(err.Error(), "log.level") {
		t.Fatalf("an explicitly empty override must be validated, got %v", err)
	}
}

func TestLoadDoesNotExposeMalformedEnvContents(t *testing.T) {
	envPath := writeFile(t, ".env", "BROKEN@KEY=do-not-print-this\n")
	_, err := load(writeFile(t, "agent.yaml", validYAML), envPath, environment(nil))
	if err == nil || !strings.Contains(err.Error(), ".env") {
		t.Fatalf("expected .env parsing error, got %v", err)
	}
	if strings.Contains(err.Error(), "do-not-print-this") {
		t.Fatal("error exposes the contents of the .env file")
	}
}

func TestTelegramSettings(t *testing.T) {
	path := writeFile(t, "agent.yaml", validYAML+"telegram:\n  enabled: false\n  allowed_user_ids: [7]\n")
	envPath := writeFile(t, ".env", "AGENT_TELEGRAM_ENABLED=true\nAGENT_BOT_TOKEN=123:fake-token\nAGENT_ALLOWED_USER_IDS=42\n")
	cfg, err := load(path, envPath, environment(map[string]string{"AGENT_ALLOWED_USER_IDS": "84, 85"}))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Telegram.Enabled || cfg.Telegram.Token != "123:fake-token" ||
		len(cfg.Telegram.AllowedUserIDs) != 2 || cfg.Telegram.AllowedUserIDs[0] != 84 {
		t.Fatal("Telegram settings did not respect environment precedence")
	}
	encoded, err := json.Marshal(cfg.Telegram)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), cfg.Telegram.Token) {
		t.Fatal("token is included in serialized settings")
	}
}

func TestTelegramRejectsInvalidSettings(t *testing.T) {
	cases := []struct {
		name  string
		patch map[string]string
		want  string
	}{
		{"missing token", map[string]string{"AGENT_BOT_TOKEN": ""}, "AGENT_BOT_TOKEN"},
		{"invalid token", map[string]string{"AGENT_BOT_TOKEN": "do-not-print-this"}, "AGENT_BOT_TOKEN"},
		{"empty allowlist", map[string]string{"AGENT_ALLOWED_USER_IDS": ""}, "allowed_user_ids"},
		{"negative ID", map[string]string{"AGENT_ALLOWED_USER_IDS": "-42"}, "positive"},
		{"zero ID", map[string]string{"AGENT_ALLOWED_USER_IDS": "0"}, "positive"},
		{"duplicate ID", map[string]string{"AGENT_ALLOWED_USER_IDS": "42,42"}, "unique"},
		{"non numeric ID", map[string]string{"AGENT_ALLOWED_USER_IDS": "andre"}, "numeric"},
		{"invalid enable flag", map[string]string{"AGENT_TELEGRAM_ENABLED": "maybe"}, "true or false"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			values := map[string]string{
				"AGENT_TELEGRAM_ENABLED": "true", "AGENT_BOT_TOKEN": "123:fake-token", "AGENT_ALLOWED_USER_IDS": "42",
			}
			for key, value := range tc.patch {
				values[key] = value
			}
			_, err := load(writeFile(t, "agent.yaml", validYAML), "", environment(values))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q error, got %v", tc.want, err)
			}
			if strings.Contains(err.Error(), "do-not-print-this") {
				t.Fatal("invalid token leaked into the error")
			}
		})
	}
}

func environment(values map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
}

func writeFile(t *testing.T, name, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
