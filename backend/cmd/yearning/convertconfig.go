package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// runConvertConfig converts legacy conf.toml into v4 YAML configuration:
//   - Mysql.* -> database.* (password/path left empty with comments pointing to env var injection,
//     never writing plaintext secrets; output keys must be accepted by config.Load KnownFields strict decoding — SEC-0050);
//   - Legacy General RPC address settings are deprecated and emitted as comments (A001: v4 has no RPC channel);
//   - General.SecretKey is referenced as a note for legacy_secret_key (used after RCP approval);
//   - Oidc is preserved as a comment block pending manual migration.
func runConvertConfig(args []string) error {
	fs := flag.NewFlagSet("migrate convert-config", flag.ContinueOnError)
	in := fs.String("source-config", "", "legacy conf.toml path")
	out := fs.String("output", "", "target yaml path; '-' for stdout")
	if err := fs.Parse(args); err != nil {
		return exitError(exitUsage, err.Error())
	}
	if *in == "" || *out == "" {
		return usageError("--source-config and --output are required")
	}
	raw, err := os.ReadFile(*in)
	if err != nil {
		return exitError(exitUsage, err.Error())
	}
	var legacy struct {
		General struct {
			SecretKey string `toml:"SecretKey"`
			Host      string `toml:"Host"`
			LogLevel  string `toml:"LogLevel"`
			Lang      string `toml:"Lang"`
		} `toml:"General"`
		Mysql struct {
			Host     string `toml:"Host"`
			User     string `toml:"User"`
			Password string `toml:"Password"`
			Db       string `toml:"Db"`
			Port     string `toml:"Port"`
		} `toml:"Mysql"`
		Oidc struct {
			Enable   bool   `toml:"Enable"`
			Callback string `toml:"Callback"`
		} `toml:"Oidc"`
	}
	if err := toml.Unmarshal(raw, &legacy); err != nil {
		return exitError(exitUsage, "parse legacy toml: "+err.Error())
	}
	var b strings.Builder
	b.WriteString("# Yearning v4 configuration (converted from legacy conf.toml)\n")
	b.WriteString("# Complete the placeholders below before starting: inject all secrets via environment variables or files.\n")
	// SEC-0050: Output keys must strictly fall within the YAML schema of config.Config
	// (config.Load rejects unknown keys with KnownFields(true)). mode is a CLI subcommand
	// argument (yearning serve) rather than a config key; password/path are left empty and injected via env vars.
	b.WriteString("database:\n")
	fmt.Fprintf(&b, "  host: %q\n", legacy.Mysql.Host)
	fmt.Fprintf(&b, "  port: %q\n", legacy.Mysql.Port)
	fmt.Fprintf(&b, "  user: %q\n", legacy.Mysql.User)
	fmt.Fprintf(&b, "  dbname: %q\n", legacy.Mysql.Db)
	b.WriteString("  # Legacy plaintext password is not written to this file; inject via YEARNING_DATABASE_PASSWORD env var.\n")
	b.WriteString("  password: \"\"\n")
	b.WriteString("  sslmode: require\n")
	b.WriteString("key_provider:\n")
	b.WriteString("  # Legacy General.SecretKey is used with legacy data migration (see RCP-20260825-B2-LEGACY-SECRET-KEY);\n")
	b.WriteString("  # generate a separate 32-byte file for v4 runtime keys and inject its path via YEARNING_KEY_PROVIDER_PATH env var.\n")
	b.WriteString("  path: \"\"\n")
	b.WriteString("session:\n")
	b.WriteString("  secure: true\n")
	b.WriteString("\n# Deprecated legacy settings (removed during conversion, must not be carried into v4):\n")
	b.WriteString("#   Legacy General RPC address setting: RPC channel removed in v4, value not migrated.\n")
	if legacy.Oidc.Enable {
		fmt.Fprintf(&b, "#   Oidc.Enable = true (OIDC Provider must be reconfigured via admin console after v4 launch, legacy callback=%q)\n", legacy.Oidc.Callback)
	}
	result := b.String()
	if *out == "-" {
		emitJSON(map[string]any{"yaml": result})
		return nil
	}
	if err := os.WriteFile(*out, []byte(result), 0o600); err != nil {
		return exitError(exitInternal, err.Error())
	}
	emitJSON(map[string]any{"output": *out, "plaintext_secrets_written": false})
	return nil
}
