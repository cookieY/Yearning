package main

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/cookieY/YearningX/backend/internal/config"
)

// TestConvertConfigFromLegacyToml smoke test: legacy conf.toml (including deprecated RPC address keys)
// is converted to v4 YAML; plaintext secrets must not be written to disk, and deprecated items are
// rendered as static notices (zero A001 literals).
// SEC-0050: Output keys must fall within the YAML schema of config.Config — keys rejected by
// strict decoding (KnownFields) such as mode/password_env/path_env must not appear, and the
// converted output as a whole must be accepted by config.Load(serve) (with password/path injected via env vars).
func TestConvertConfigFromLegacyToml(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "conf.toml")
	legacy := "[General]\n" +
		"SecretKey = \"0123456789abcdef\"\n" +
		"Host = \"0.0.0.0\"\n" +
		"RpcAddr = \"127.0.0.1:9001\"\n" +
		"LogLevel = \"info\"\n" +
		"Lang = \"cn\"\n\n" +
		"[Mysql]\n" +
		"Host = \"127.0.0.1\"\n" +
		"User = \"yearning\"\n" +
		"Password = \"legacy-plaintext-pw\"\n" +
		"Db = \"yearning\"\n" +
		"Port = \"3306\"\n\n" +
		"[Oidc]\n" +
		"Enable = true\n" +
		"Callback = \"https://old.example.com/cb\"\n"
	if err := os.WriteFile(src, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "v4.yaml")
	if err := runConvertConfig([]string{"--source-config", src, "--output", out}); err != nil {
		t.Fatalf("convert-config: %v", err)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	yamlText := string(raw)
	// Plaintext secrets and legacy passwords must never be written.
	for _, secret := range []string{"0123456789abcdef", "legacy-plaintext-pw"} {
		if strings.Contains(yamlText, secret) {
			t.Fatalf("plaintext secret leaked into converted yaml: %q", secret)
		}
	}
	// SEC-0050: Keys rejected by strict decoding must not appear (mode is a CLI subcommand argument;
	// password_env/path_env are not in the config.Config schema).
	if regexp.MustCompile(`(?m)^mode:`).MatchString(yamlText) {
		t.Fatalf("converted yaml must not contain top-level mode: key (CLI arg, not config key):\n%s", yamlText)
	}
	for _, banned := range []string{"password_env", "path_env"} {
		if strings.Contains(yamlText, banned) {
			t.Fatalf("converted yaml contains schema-unknown key %q (rejected by KnownFields):\n%s", banned, yamlText)
		}
	}
	// Required structure: empty placeholders + env var injection notes + sslmode/session security defaults.
	for _, want := range []string{
		`password: ""`, `path: ""`, "sslmode: require",
		"session:", "secure: true",
		"YEARNING_DATABASE_PASSWORD", "YEARNING_KEY_PROVIDER_PATH",
	} {
		if !strings.Contains(yamlText, want) {
			t.Fatalf("converted yaml missing %q:\n%s", want, yamlText)
		}
	}
	// Deprecation notices: RPC channel removal notice; OIDC manual migration notice.
	if !strings.Contains(yamlText, "RPC") || !strings.Contains(yamlText, "Deprecated") {
		t.Fatalf("converted yaml missing RPC deprecation notice:\n%s", yamlText)
	}
	if !strings.Contains(yamlText, "OIDC") {
		t.Fatalf("converted yaml missing OIDC manual-migration notice:\n%s", yamlText)
	}

	// Loopback 1 (fail-closed): without env var injection, serve mode validation must fail
	// due to missing password/path — proving strict validation is active rather than a no-op.
	_, loadErr := config.Load(context.Background(), config.ModeServe, out)
	if loadErr == nil {
		t.Fatal("config.Load(serve) must fail while password/path env vars are unset")
	}
	for _, missing := range []string{"database.password", "key_provider.path"} {
		if !strings.Contains(loadErr.Error(), missing) {
			t.Fatalf("config.Load(serve) error must report %s, got: %v", missing, loadErr)
		}
	}
	// Loopback 2 (loadable): after injecting secrets via env vars as documented, the converted output
	// must be accepted by config.Load(serve) as a whole (including KnownFields strict decoding and all required fields).
	t.Setenv("YEARNING_DATABASE_PASSWORD", "env-injected-password")
	t.Setenv("YEARNING_KEY_PROVIDER_PATH", "/var/run/secrets/yearning/keyring/keyring.json")
	cfg, err := config.Load(context.Background(), config.ModeServe, out)
	if err != nil {
		t.Fatalf("converted yaml must be loadable by config.Load(serve) with env injection: %v", err)
	}
	if cfg.Database.Host != "127.0.0.1" || cfg.Database.Port != "3306" ||
		cfg.Database.User != "yearning" || cfg.Database.DBName != "yearning" ||
		cfg.Database.SSLMode != "require" || !cfg.Session.Secure {
		t.Fatalf("converted yaml did not round-trip legacy fields correctly: %+v", cfg)
	}
	if cfg.Database.Password != "env-injected-password" ||
		cfg.KeyProvider.Path != "/var/run/secrets/yearning/keyring/keyring.json" {
		t.Fatalf("env injection did not reach config leaves: %+v", cfg)
	}
}
