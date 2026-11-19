// plan.go builds gh-ost execution plans and deterministically validates ALTER statements
// (per gh-ost-execution-contract argv_policy): all argv elements are generated server-side
// with zero user/AI flag injection; --alter is deterministically parsed from a single
// frozen ALTER statement and never passes through raw flag text.
package osc

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// ErrPlanInvalid indicates plan construction failed (fail closed; never falls back to direct DDL).
var ErrPlanInvalid = errors.New("osc: plan invalid")

// ErrOverrideInvalid indicates typed overrides are invalid (unknown key or out-of-range value).
var ErrOverrideInvalid = errors.New("osc: override invalid")

// Tool identifies the OSC tool (per osc-execution-contract tools).
type Tool string

const (
	ToolGhOst Tool = "gh-ost"
	ToolPtOsc Tool = "pt-osc"
)

// Overrides holds typed user/policy tuning keys (osc-execution-contract
// typed_overrides.keys; credentials, endpoints, and SQL can never be overridden).
type Overrides struct {
	MaxLoad                       *string  `json:"max_load,omitempty"`
	CriticalLoad                  *string  `json:"critical_load,omitempty"`
	CriticalLoadIntervalMillis    *int     `json:"critical_load_interval_millis,omitempty"`
	CriticalLoadHibernateSeconds  *int     `json:"critical_load_hibernate_seconds,omitempty"`
	ChunkSize                     *int     `json:"chunk_size,omitempty"`
	DMLBatchSize                  *int     `json:"dml_batch_size,omitempty"`
	MaxLag                        *int     `json:"max_lag,omitempty"`
	NiceRatio                     *float64 `json:"nice_ratio,omitempty"`
	CutOverLockTimeoutSeconds     *int     `json:"cut_over_lock_timeout_seconds,omitempty"`
	Retries                       *int     `json:"retries,omitempty"`
	HeartbeatIntervalMillis       *int     `json:"heartbeat_interval_millis,omitempty"`
	ExponentialBackoffMaxInterval *int     `json:"exponential_backoff_max_interval,omitempty"`
	ExactRowCount                 *bool    `json:"exact_rowcount,omitempty"`
	ConcurrentRowCount            *bool    `json:"concurrent_rowcount,omitempty"`
	OkToDropTable                 *bool    `json:"ok_to_drop_table,omitempty"`
	TimestampOldTable             *bool    `json:"timestamp_old_table,omitempty"`
	CutOverExponentialBackoff     *bool    `json:"cut_over_exponential_backoff,omitempty"`
	AttemptInstantDDL             *bool    `json:"attempt_instant_ddl,omitempty"`
}

// loadPattern defines the valid format for max_load/critical_load (comma-separated status=value pairs).
var loadPattern = regexp.MustCompile(`^[A-Za-z_]+=[0-9]{1,9}(,[A-Za-z_]+=[0-9]{1,9})*$`)

// OverrideKeySet is the set of valid override keys (unknown keys are rejected, never silently dropped).
var OverrideKeySet = map[string]bool{
	"max_load":                         true,
	"critical_load":                    true,
	"critical_load_interval_millis":    true,
	"critical_load_hibernate_seconds":  true,
	"chunk_size":                       true,
	"dml_batch_size":                   true,
	"max_lag":                          true,
	"nice_ratio":                       true,
	"cut_over_lock_timeout_seconds":    true,
	"retries":                          true,
	"heartbeat_interval_millis":        true,
	"exponential_backoff_max_interval": true,
	"exact_rowcount":                   true,
	"concurrent_rowcount":              true,
	"ok_to_drop_table":                 true,
	"timestamp_old_table":              true,
	"cut_over_exponential_backoff":     true,
	"attempt_instant_ddl":              true,
}

// ParseOverrides parses and validates overrides from an untyped map (fails closed on unknown keys or invalid values).
func ParseOverrides(raw map[string]any) (*Overrides, error) {
	if raw == nil {
		return &Overrides{}, nil
	}
	out := &Overrides{}
	for k, v := range raw {
		if !OverrideKeySet[k] {
			return nil, fmt.Errorf("%w: unknown key %s", ErrOverrideInvalid, k)
		}
		switch k {
		case "max_load", "critical_load":
			str, ok := v.(string)
			if !ok || !loadPattern.MatchString(str) {
				return nil, fmt.Errorf("%w: %s value", ErrOverrideInvalid, k)
			}
			if k == "max_load" {
				out.MaxLoad = &str
			} else {
				out.CriticalLoad = &str
			}
		case "nice_ratio":
			var fv float64
			var ok bool
			switch n := v.(type) {
			case float64:
				fv, ok = n, true
			case int:
				fv, ok = float64(n), true
			}
			if !ok || fv < 0 || fv > 100 {
				return nil, fmt.Errorf("%w: %s out of range", ErrOverrideInvalid, k)
			}
			out.NiceRatio = &fv
		case "exact_rowcount", "concurrent_rowcount", "ok_to_drop_table",
			"timestamp_old_table", "cut_over_exponential_backoff", "attempt_instant_ddl":
			bv, ok := v.(bool)
			if !ok {
				return nil, fmt.Errorf("%w: %s value", ErrOverrideInvalid, k)
			}
			switch k {
			case "exact_rowcount":
				out.ExactRowCount = &bv
			case "concurrent_rowcount":
				out.ConcurrentRowCount = &bv
			case "ok_to_drop_table":
				out.OkToDropTable = &bv
			case "timestamp_old_table":
				out.TimestampOldTable = &bv
			case "cut_over_exponential_backoff":
				out.CutOverExponentialBackoff = &bv
			case "attempt_instant_ddl":
				out.AttemptInstantDDL = &bv
			}
		case "chunk_size", "dml_batch_size", "max_lag", "retries",
			"critical_load_interval_millis", "critical_load_hibernate_seconds",
			"cut_over_lock_timeout_seconds", "heartbeat_interval_millis",
			"exponential_backoff_max_interval":
			// JSON decodes numbers as float64; native Go callers may pass int.
			iv := -1
			switch n := v.(type) {
			case float64:
				if n == float64(int(n)) {
					iv = int(n)
				}
			case int:
				iv = n
			}
			if iv < 0 {
				return nil, fmt.Errorf("%w: %s value", ErrOverrideInvalid, k)
			}
			lo, hi := 0, 0
			switch k {
			case "chunk_size":
				lo, hi = 1, 100000
			case "dml_batch_size":
				lo, hi = 1, 1000
			case "max_lag":
				lo, hi = 0, 86400
			case "retries":
				lo, hi = 1, 120
			case "critical_load_interval_millis":
				lo, hi = 0, 60000
			case "critical_load_hibernate_seconds":
				lo, hi = 0, 86400
			case "cut_over_lock_timeout_seconds":
				lo, hi = 1, 30
			case "heartbeat_interval_millis":
				lo, hi = 50, 10000
			case "exponential_backoff_max_interval":
				lo, hi = 2, 640
			}
			if iv < lo || iv > hi {
				return nil, fmt.Errorf("%w: %s out of range", ErrOverrideInvalid, k)
			}
			switch k {
			case "chunk_size":
				out.ChunkSize = &iv
			case "dml_batch_size":
				out.DMLBatchSize = &iv
			case "max_lag":
				out.MaxLag = &iv
			case "retries":
				out.Retries = &iv
			case "critical_load_interval_millis":
				out.CriticalLoadIntervalMillis = &iv
			case "critical_load_hibernate_seconds":
				out.CriticalLoadHibernateSeconds = &iv
			case "cut_over_lock_timeout_seconds":
				out.CutOverLockTimeoutSeconds = &iv
			case "heartbeat_interval_millis":
				out.HeartbeatIntervalMillis = &iv
			case "exponential_backoff_max_interval":
				out.ExponentialBackoffMaxInterval = &iv
			}
		}
	}
	return out, nil
}

// Merge returns the effective tuning set after applying overrides on top of defaults.
func Merge(defaults, overrides *Overrides) *Overrides {
	out := &Overrides{}
	if defaults != nil {
		*out = *defaults
	}
	if overrides == nil {
		return out
	}
	if overrides.MaxLoad != nil {
		out.MaxLoad = overrides.MaxLoad
	}
	if overrides.CriticalLoad != nil {
		out.CriticalLoad = overrides.CriticalLoad
	}
	if overrides.CriticalLoadIntervalMillis != nil {
		out.CriticalLoadIntervalMillis = overrides.CriticalLoadIntervalMillis
	}
	if overrides.CriticalLoadHibernateSeconds != nil {
		out.CriticalLoadHibernateSeconds = overrides.CriticalLoadHibernateSeconds
	}
	if overrides.ChunkSize != nil {
		out.ChunkSize = overrides.ChunkSize
	}
	if overrides.DMLBatchSize != nil {
		out.DMLBatchSize = overrides.DMLBatchSize
	}
	if overrides.MaxLag != nil {
		out.MaxLag = overrides.MaxLag
	}
	if overrides.NiceRatio != nil {
		out.NiceRatio = overrides.NiceRatio
	}
	if overrides.CutOverLockTimeoutSeconds != nil {
		out.CutOverLockTimeoutSeconds = overrides.CutOverLockTimeoutSeconds
	}
	if overrides.Retries != nil {
		out.Retries = overrides.Retries
	}
	if overrides.HeartbeatIntervalMillis != nil {
		out.HeartbeatIntervalMillis = overrides.HeartbeatIntervalMillis
	}
	if overrides.ExponentialBackoffMaxInterval != nil {
		out.ExponentialBackoffMaxInterval = overrides.ExponentialBackoffMaxInterval
	}
	if overrides.ExactRowCount != nil {
		out.ExactRowCount = overrides.ExactRowCount
	}
	if overrides.ConcurrentRowCount != nil {
		out.ConcurrentRowCount = overrides.ConcurrentRowCount
	}
	if overrides.OkToDropTable != nil {
		out.OkToDropTable = overrides.OkToDropTable
	}
	if overrides.TimestampOldTable != nil {
		out.TimestampOldTable = overrides.TimestampOldTable
	}
	if overrides.CutOverExponentialBackoff != nil {
		out.CutOverExponentialBackoff = overrides.CutOverExponentialBackoff
	}
	if overrides.AttemptInstantDDL != nil {
		out.AttemptInstantDDL = overrides.AttemptInstantDDL
	}
	return out
}

// Argv maps override keys to tool-specific CLI flag arguments (server-assembled; nil-safe).
func (o *Overrides) Argv(tool Tool) []string {
	if o == nil {
		return nil
	}
	var out []string
	addStr := func(flag string, v *string) {
		if v != nil {
			out = append(out, flag+"="+*v)
		}
	}
	addInt := func(flag string, v *int) {
		if v != nil {
			out = append(out, fmt.Sprintf("%s=%d", flag, *v))
		}
	}
	addFloat := func(flag string, v *float64) {
		if v != nil {
			out = append(out, fmt.Sprintf("%s=%g", flag, *v))
		}
	}
	addBool := func(flag string, v *bool) {
		if v != nil {
			out = append(out, fmt.Sprintf("%s=%t", flag, *v))
		}
	}
	if tool == ToolPtOsc {
		addStr("--max-load", o.MaxLoad)
		addStr("--critical-load", o.CriticalLoad)
		addInt("--chunk-size", o.ChunkSize)
		addInt("--max-lag", o.MaxLag)
		addInt("--retries", o.Retries)
		return out
	}
	addStr("--max-load", o.MaxLoad)
	addStr("--critical-load", o.CriticalLoad)
	addInt("--critical-load-interval-millis", o.CriticalLoadIntervalMillis)
	addInt("--critical-load-hibernate-seconds", o.CriticalLoadHibernateSeconds)
	addInt("--chunk-size", o.ChunkSize)
	addInt("--dml-batch-size", o.DMLBatchSize)
	addInt("--max-lag-millis", o.MaxLag)
	addFloat("--nice-ratio", o.NiceRatio)
	addInt("--cut-over-lock-timeout-seconds", o.CutOverLockTimeoutSeconds)
	addInt("--default-retries", o.Retries)
	addInt("--heartbeat-interval-millis", o.HeartbeatIntervalMillis)
	addInt("--exponential-backoff-max-interval", o.ExponentialBackoffMaxInterval)
	addBool("--exact-rowcount", o.ExactRowCount)
	addBool("--concurrent-rowcount", o.ConcurrentRowCount)
	addBool("--ok-to-drop-table", o.OkToDropTable)
	addBool("--timestamp-old-table", o.TimestampOldTable)
	addBool("--cut-over-exponential-backoff", o.CutOverExponentialBackoff)
	addBool("--attempt-instant-ddl", o.AttemptInstantDDL)
	return out
}

// PlanInput holds the frozen inputs for building an execution plan.
type PlanInput struct {
	Database string // Target database (resolved from frozen mapping)
	Table    string // Target table (parsed from frozen statement)
	Alter    string // ALTER clause (deterministically parsed from frozen statement)
	Host     string
	Port     int
	User     string
}

// Plan represents a deterministically generated execution plan (free of any credential material).
type Plan struct {
	Argv       []string `json:"argv"`
	AttemptDir string   `json:"attempt_dir"`
}

// forbiddenFlags lists flags explicitly prohibited by contract (presence fails plan construction).
var forbiddenFlags = map[string]bool{
	"--password": true, "--allow-on-master": true, "--skip-foreign-key-checks": true,
	"--allow-nullable-unique-key": true, "--approve-renamed-columns": true,
	"--initially-drop-ghost-table": true, "--initially-drop-old-table": true,
}

// ValidateArgv verifies that argv satisfies the contract flag surface: required flags present,
// forbidden flags absent, and passwords excluded. Used for test injection (production argv is only produced by BuildArgv).
func ValidateArgv(argv []string) error {
	required := map[string]bool{
		"--database": false, "--table": false, "--alter": false, "--host": false,
		"--port": false, "--user": false, "--ask-pass": false, "--assume-rbr": false,
		"--panic-flag-file": false, "--postpone-cut-over-flag-file": false,
		"--serve-socket-file": false,
	}
	for _, a := range argv {
		name := a
		if i := strings.Index(a, "="); i >= 0 {
			name = a[:i]
		}
		if forbiddenFlags[name] {
			return fmt.Errorf("%w: forbidden flag %s", ErrPlanInvalid, name)
		}
		if _, ok := required[name]; ok {
			required[name] = true
		} else if strings.HasPrefix(name, "--") && !allowedAuxFlags[name] {
			return fmt.Errorf("%w: unknown flag %s", ErrPlanInvalid, name)
		}
	}
	for name, seen := range required {
		if !seen {
			return fmt.Errorf("%w: missing flag %s", ErrPlanInvalid, name)
		}
	}
	return nil
}

// allowedAuxFlags lists non-mandatory auxiliary flags allowed by the whitelist (all others are rejected).
var allowedAuxFlags = map[string]bool{
	"--cut-over":                         true,
	"--exact-rowcount":                   true,
	"--concurrent-rowcount":              true,
	"--default-retries":                  true,
	"--chunk-size":                       true,
	"--dml-batch-size":                   true,
	"--max-load":                         true,
	"--critical-load":                    true,
	"--critical-load-interval-millis":    true,
	"--critical-load-hibernate-seconds":  true,
	"--max-lag-millis":                   true,
	"--nice-ratio":                       true,
	"--cut-over-lock-timeout-seconds":    true,
	"--exponential-backoff-max-interval": true,
	"--ok-to-drop-table":                 true,
	"--timestamp-old-table":              true,
	"--cut-over-exponential-backoff":     true,
	"--attempt-instant-ddl":              true,
	"--panic-flag-file":                  true,
	"--postpone-cut-over-flag-file":      true,
	"--serve-socket-file":                true,
	"--serve-tcp-port":                   true,
	"--heartbeat-interval-millis":        true,
}

// fileNames returns control file paths (server-generated absolute paths inside the attempt directory).
type fileNames struct {
	PanicFlag       string
	PostponeCutover string
	ServeSocket     string
}

// BuildArgv builds the complete argv for gh-ost (v1 compatibility alias).
func BuildArgv(in PlanInput, dir string) ([]string, fileNames, error) {
	return BuildGhOstArgv(in, dir, nil)
}

// BuildGhOstArgv builds the complete argv for gh-ost (mapping override keys onto the flag surface).
func BuildGhOstArgv(in PlanInput, dir string, ov *Overrides) ([]string, fileNames, error) {
	if err := ValidateIdent(in.Database); err != nil {
		return nil, fileNames{}, err
	}
	if err := ValidateIdent(in.Table); err != nil {
		return nil, fileNames{}, err
	}
	if strings.TrimSpace(in.Alter) == "" {
		return nil, fileNames{}, fmt.Errorf("%w: empty alter clause", ErrPlanInvalid)
	}
	if strings.ContainsAny(in.Alter, "\x00;") {
		return nil, fileNames{}, fmt.Errorf("%w: alter clause invalid characters", ErrPlanInvalid)
	}
	if in.Host == "" || in.Port <= 0 || in.Port > 65535 {
		return nil, fileNames{}, fmt.Errorf("%w: endpoint invalid", ErrPlanInvalid)
	}
	if in.User == "" {
		return nil, fileNames{}, fmt.Errorf("%w: user empty", ErrPlanInvalid)
	}
	if dir == "" {
		return nil, fileNames{}, fmt.Errorf("%w: attempt dir empty", ErrPlanInvalid)
	}
	files := fileNames{
		PanicFlag:       dir + "/panic-flag",
		PostponeCutover: dir + "/postpone-cutover",
		ServeSocket:     dir + "/serve.sock",
	}
	argv := []string{
		"--database=" + in.Database,
		"--table=" + in.Table,
		"--alter=" + in.Alter,
		"--host=" + in.Host,
		fmt.Sprintf("--port=%d", in.Port),
		"--user=" + in.User,
		"--ask-pass",
		"--assume-rbr",
		"--panic-flag-file=" + files.PanicFlag,
		"--postpone-cut-over-flag-file=" + files.PostponeCutover,
		"--serve-socket-file=" + files.ServeSocket,
	}
	argv = append(argv, ov.Argv(ToolGhOst)...)
	if err := ValidateArgv(argv); err != nil {
		return nil, fileNames{}, err
	}
	return argv, files, nil
}

// ptOscForbiddenPrefixes lists rejected prefixes for pt-osc (actual flag forms: --no-... /
// --[no]check-* variants matched by prefix; any form of --password is rejected).
var ptOscForbiddenPrefixes = []string{
	"--password", "--set-vars", "--drop-old-table", "--no-drop-old-table",
	"--check-replication-filters", "--no-check-replication-filters",
	"--recursion-method",
}

// ValidatePtOscArgv validates pt-osc argv: required flags present, all 5 connection DSN keys present,
// and forbidden flags absent. The DSN is a single argument (h=..,P=..,u=..,D=..,t=..).
func ValidatePtOscArgv(argv []string) error {
	hasAlter, hasExecute, hasAskPass, hasDSN := false, false, false, false
	dsnKeys := map[string]bool{"h=": false, "P=": false, "u=": false, "D=": false, "t=": false}
	for _, a := range argv {
		for _, prefix := range ptOscForbiddenPrefixes {
			if strings.HasPrefix(a, prefix) {
				return fmt.Errorf("%w: forbidden pt-osc flag %s", ErrPlanInvalid, a)
			}
		}
		switch {
		case strings.HasPrefix(a, "--alter="):
			hasAlter = true
		case a == "--execute":
			hasExecute = true
		case a == "--ask-pass":
			hasAskPass = true
		case strings.Contains(a, "h=") && strings.Contains(a, ","):
			// Connection DSN element: all five keys must be present.
			for k := range dsnKeys {
				dsnKeys[k] = strings.Contains(a, k)
			}
			hasDSN = true
		}
	}
	if !hasAlter || !hasExecute || !hasAskPass || !hasDSN {
		return fmt.Errorf("%w: missing pt-osc required args", ErrPlanInvalid)
	}
	for k, seen := range dsnKeys {
		if !seen {
			return fmt.Errorf("%w: pt-osc DSN missing %s", ErrPlanInvalid, k)
		}
	}
	return nil
}

// BuildPtOscArgv builds the complete argv for pt-online-schema-change
// (connection DSN short keys h/P/u/D/t + --execute; password passed exclusively via --ask-pass stdin).
func BuildPtOscArgv(in PlanInput, ov *Overrides) ([]string, error) {
	if err := ValidateIdent(in.Database); err != nil {
		return nil, err
	}
	if err := ValidateIdent(in.Table); err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.Alter) == "" || strings.ContainsAny(in.Alter, "\x00;") {
		return nil, fmt.Errorf("%w: alter clause invalid", ErrPlanInvalid)
	}
	if in.Host == "" || in.Port <= 0 || in.Port > 65535 || in.User == "" {
		return nil, fmt.Errorf("%w: endpoint invalid", ErrPlanInvalid)
	}
	argv := []string{
		"--alter=" + in.Alter,
		"--execute",
		"--ask-pass",
		fmt.Sprintf("h=%s,P=%d,u=%s,D=%s,t=%s",
			in.Host, in.Port, in.User, in.Database, in.Table),
	}
	argv = append(argv, ov.Argv(ToolPtOsc)...)
	if err := ValidatePtOscArgv(argv); err != nil {
		return nil, err
	}
	return argv, nil
}

// PlanHash computes a deterministic content hash of the plan (argv digest without ciphertext or credentials).
func PlanHash(argv []string, dir string) string {
	h := sha256.Sum256([]byte(strings.Join(argv, "\x00") + "\x00" + dir))
	return hex.EncodeToString(h[:])
}

// ParseAlterTable deterministically parses (database, table, alterClause) from a single frozen
// ALTER TABLE statement. Supports `db`.`tbl`, db.tbl, and `tbl` table forms; fails closed on parse failure (never guesses).
func ParseAlterTable(statement, defaultDatabase string) (db, table, alter string, err error) {
	s := strings.TrimSpace(statement)
	s = strings.TrimLeft(s, "( \t\r\n")
	// Strip balanced leading wrapper parentheses (nested statement form) to avoid polluting the end of the alter clause.
	if strings.HasPrefix(strings.TrimSpace(statement), "(") && strings.HasSuffix(s, ")") {
		s = s[:len(s)-1]
	}
	if !strings.HasPrefix(strings.ToUpper(s), "ALTER TABLE") {
		return "", "", "", fmt.Errorf("%w: not alter table", ErrPlanInvalid)
	}
	rest := strings.TrimSpace(s[len("ALTER TABLE"):])
	// Parse table name token (backtick-quoted segments or bare identifiers, optionally dot-qualified).
	db, table, rest, err = parseQualifiedTable(rest)
	if err != nil {
		return "", "", "", err
	}
	if db == "" {
		db = defaultDatabase
	}
	if db == "" {
		return "", "", "", fmt.Errorf("%w: database unresolvable", ErrPlanInvalid)
	}
	alter = strings.TrimSpace(rest)
	alter = strings.TrimRight(alter, "; \t\r\n")
	if alter == "" {
		return "", "", "", fmt.Errorf("%w: empty alter clause", ErrPlanInvalid)
	}
	if strings.Contains(alter, ";") {
		return "", "", "", fmt.Errorf("%w: multiple statements", ErrPlanInvalid)
	}
	return db, table, alter, nil
}

// parseQualifiedTable parses a dot-qualified table name and returns the remaining clause.
func parseQualifiedTable(s string) (db, table, rest string, err error) {
	// Backtick-quoted identifier.
	readBacktick := func(str string) (string, string, bool) {
		if !strings.HasPrefix(str, "`") {
			return "", str, false
		}
		end := strings.Index(str[1:], "`")
		if end < 0 {
			return "", str, false
		}
		return strings.ReplaceAll(str[1:end+1], "``", "`"), str[end+2:], true
	}
	readBare := func(str string) (string, string, bool) {
		i := 0
		for i < len(str) {
			c := str[i]
			if c == '_' || c == '$' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
				(c >= '0' && c <= '9' && i > 0) {
				i++
				continue
			}
			break
		}
		if i == 0 {
			return "", str, false
		}
		return str[:i], str[i:], true
	}
	readIdent := func(str string) (string, string, bool) {
		if v, r, ok := readBacktick(str); ok {
			return v, r, true
		}
		return readBare(str)
	}
	first, r1, ok := readIdent(s)
	if !ok {
		return "", "", "", fmt.Errorf("%w: table name unparseable", ErrPlanInvalid)
	}
	r1 = strings.TrimLeft(r1, " \t\r\n")
	db, table, rest = "", first, r1
	if strings.HasPrefix(r1, ".") {
		second, r2, ok2 := readIdent(strings.TrimPrefix(r1, "."))
		if !ok2 {
			return "", "", "", fmt.Errorf("%w: qualified table unparseable", ErrPlanInvalid)
		}
		db, table, rest = first, second, strings.TrimLeft(r2, " \t\r\n")
	}
	if table == "" {
		return "", "", "", fmt.Errorf("%w: table name empty", ErrPlanInvalid)
	}
	if err := ValidateIdent(table); err != nil {
		return "", "", "", err
	}
	return db, table, rest, nil
}

// ValidateIdent validates an identifier against the whitelist (rejects empty, overlong, or invalid characters).
func ValidateIdent(s string) error {
	if s == "" || len(s) > 128 {
		return fmt.Errorf("%w: identifier invalid", ErrPlanInvalid)
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '_' || c == '$' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') {
			continue
		}
		return fmt.Errorf("%w: identifier character", ErrPlanInvalid)
	}
	return nil
}
