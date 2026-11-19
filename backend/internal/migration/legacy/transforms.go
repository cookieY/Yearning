package legacy

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/cookieY/YearningX/backend/internal/crypto"
)

// TransformContext holds all shared state for a single migration run.
type TransformContext struct {
	RunID    uuid.UUID
	Crypter  *Crypter
	Timezone *time.Location
	Now      time.Time

	// Resolution indexes (first-seen wins; construction order follows SourceTables).
	Users          map[string]uuid.UUID // username(lower) → users.id
	Usernames      map[uuid.UUID]string
	Datasources    map[string]uuid.UUID // source_id → datasources.id
	DSFingerprint  map[uuid.UUID]string
	Orders         map[string]uuid.UUID    // work_id → change_orders.id
	QueryOrders    map[string]uuid.UUID    // work_id → query_access_requests.id
	QuerySessions  map[string]uuid.UUID    // query work_id → synthetic session id
	Records        map[string][]recordMeta // work_id -> execution records (for ambiguous status adjudication)
	FlowOfDS       map[string]uuid.UUID    // source_id -> confirmed candidate query flow
	OrderSnapshots map[string]map[string]any
	QuerySnapshots map[string]map[string]any
	WorkflowEvents map[string][]string
	// pendingRollbacks buffers legacy rollback SQL statements in arrival order (ascending source row ID);
	// FinalizeRollbackBundles merges them into the corresponding ticket snapshots and reseals them after all
	// table transforms complete, ensuring rollback_bundle is sealed inside order_flow_snapshots ciphertext (E003/M002).
	pendingRollbacks    map[string][]string
	attemptOfOrder      map[string]uuid.UUID
	orderDatasource     map[string]string
	ordinalCounter      map[string]int64
	sequenceCounter     map[string]int64
	legacyAuditRoleHash string
	groupIDs            map[string]uuid.UUID // legacy group name / GroupId -> permission_groups.id
	groupGrants         map[string]any
	flowTemplates       []flowTemplateRef
	ruleSets            []uuid.UUID
	nameSeen            map[string]int

	ExistingBuiltinAdmin  uuid.UUID // Bootstrapped builtin admin (zero UUID if none)
	AdminMergedToExisting bool

	// Write buffer (flushed in batches during apply; counted without writing during dry-run).
	writes              []WriteOp
	EncryptedFieldCount int64
	Quarantine          []QuarantineRecord
	Report              map[string]any // migration_report jsonb (redacted)
	Candidates          []ManifestCandidate
}

// WriteOp binds a target table name to an explicit GORM model instance.
type WriteOp struct {
	Table string
	Model any
}

type flowTemplateRef struct {
	Key        string
	Datasource uuid.UUID
	Multi      bool
	Source     string
}

type recordMeta struct {
	Success    bool
	HasError   bool
	FinishedAt time.Time
}

// QuarantineRecord represents a quarantined source row (contains no plaintext content).
type QuarantineRecord struct {
	Table  string
	Key    string
	Reason string
}

// NewTransformContext constructs an empty TransformContext.
func NewTransformContext(runID uuid.UUID, c *Crypter, tz *time.Location) *TransformContext {
	return &TransformContext{
		RunID: runID, Crypter: c, Timezone: tz, Now: time.Now().UTC(),
		Users: map[string]uuid.UUID{}, Usernames: map[uuid.UUID]string{},
		Datasources: map[string]uuid.UUID{}, DSFingerprint: map[uuid.UUID]string{},
		Orders: map[string]uuid.UUID{}, QueryOrders: map[string]uuid.UUID{},
		QuerySessions: map[string]uuid.UUID{}, Records: map[string][]recordMeta{},
		FlowOfDS: map[string]uuid.UUID{}, OrderSnapshots: map[string]map[string]any{},
		QuerySnapshots: map[string]map[string]any{}, WorkflowEvents: map[string][]string{},
		pendingRollbacks: map[string][]string{},
		attemptOfOrder:   map[string]uuid.UUID{}, orderDatasource: map[string]string{},
		ordinalCounter: map[string]int64{}, sequenceCounter: map[string]int64{},
		groupIDs:    map[string]uuid.UUID{},
		groupGrants: map[string]any{}, nameSeen: map[string]int{},
		Report: map[string]any{},
	}
}

func (tc *TransformContext) addWrite(table string, model any) {
	tc.writes = append(tc.writes, WriteOp{Table: table, Model: model})
}

func (tc *TransformContext) appendCandidate(c ManifestCandidate) {
	tc.Candidates = append(tc.Candidates, c)
}

// builtinAdminID returns the deterministic UUID for the builtin admin (claimed when username=admin is transformed).
func (tc *TransformContext) builtinAdminID() string {
	if id, ok := tc.Users["admin"]; ok {
		return id.String()
	}
	return uuid.NewSHA1(tc.RunID, []byte("core_accounts|admin")).String()
}

// uniqueName ensures name uniqueness (enforcing unique name semantics for permission_groups and rule_sets).
func (tc *TransformContext) uniqueName(base string) string {
	tc.nameSeen[base]++
	if n := tc.nameSeen[base]; n > 1 {
		return fmt.Sprintf("%s-%d", base, n)
	}
	return base
}

func (tc *TransformContext) quarantine(table, key, reason string) {
	tc.Quarantine = append(tc.Quarantine, QuarantineRecord{Table: table, Key: key, Reason: reason})
}

func (tc *TransformContext) sealJSON(domain string, aad crypto.AAD, v any) (Sealed, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return Sealed{}, err
	}
	return tc.sealBytes(domain, aad, raw)
}

// sealBytes seals plaintext with a runtime AAD; aad is constructed by tc.aad and must match
// the corresponding runtime reader field for field (RCP-20260831-LEGACY-MIGRATION-AAD Option A).
func (tc *TransformContext) sealBytes(domain string, aad crypto.AAD, raw []byte) (Sealed, error) {
	s, err := tc.Crypter.Seal(raw, aad)
	if err != nil {
		return Sealed{}, err
	}
	tc.EncryptedFieldCount++
	return s, nil
}

// aad constructs a domain AAD bound to this deployment ID (mirroring table/primaryKey/column/purpose of the runtime reader).
func (tc *TransformContext) aad(table, primaryKey, column, purpose string) crypto.AAD {
	return tc.Crypter.AAD(table, primaryKey, column, purpose)
}

// detID deterministically derives a UUID from (runID, table, sourceKey) so Dry Runs are repeatable and reconciliation is stable.
func (tc *TransformContext) detID(table, key string) uuid.UUID {
	return uuid.NewSHA1(tc.RunID, []byte(table+"|"+key))
}

// parseLegacyTime parses a legacy database timestamp into UTC using source_timezone.
// Supports both time.Time (from MySQL parseTime) and legacy string formats.
//
// Invariant: any timestamp representation without an explicit offset maps "the same wall-clock digits + source_timezone -> UTC".
// Legacy DATETIME columns carry no time zone, and OpenMySQL's DSN does not set a loc parameter,
// so go-sql-driver parses them as UTC and tags the resulting time.Time with time.UTC -- that zone tag is synthetic,
// while the true meaning is the legacy database's local wall-clock time. Therefore the time.Time branch cannot
// simply call t.UTC(); it must discard the driver's synthetic zone tag and reinterpret the wall-clock digits in tc.Timezone.
//
// Strings carrying an explicit offset (RFC3339) are exempt from this invariant: the offset is part of the datum itself
// and is interpreted directly without layering source_timezone on top.
//
// If a loc parameter were ever added to the DSN in the future, the driver's returned zone would no longer be time.UTC,
// and the t.Location() != time.UTC guard below will fail closed rather than silently double-shifting the offset.
func (tc *TransformContext) parseLegacyTime(v any, field string) (time.Time, error) {
	switch t := v.(type) {
	case time.Time:
		// '0000-00-00 00:00:00' is converted by the driver into a zero time.Time. The string branch reports an
		// empty error for it, and this branch must behave identically; otherwise the zero value would be reinterpreted
		// as an instant in year 0 AD, bypassing the caller's fallback branch and writing terminal timestamps near year 1 AD.
		//
		// Year() <= 0 covers the entire zero-year family: '0000-00-00 12:34:56' does not match the driver's exact zero prefix,
		// so the driver returns time.Date(0,0,0,12,34,56,loc) which normalizes to year < 0 (non-IsZero but still invalid),
		// while '0000-01-15 00:00:00' parses to year == 0. Both must be rejected as invalid so the time.Time and string
		// branches remain semantically equivalent.
		if t.IsZero() || t.Year() <= 0 {
			return time.Time{}, fmt.Errorf("%s is empty or year <= 0", field)
		}
		if tc.Timezone == nil {
			return time.Time{}, fmt.Errorf("%s: source_timezone is not configured", field)
		}
		// The driver's zone tag must be time.UTC (DSN has no loc). If not, someone altered the DSN or swapped
		// the Source implementation, and reinterpreting wall-clock digits would silently shift the timestamp twice -- fail closed.
		if _, offset := t.Zone(); t.Location() != time.UTC || offset != 0 {
			return time.Time{}, fmt.Errorf(
				"%s: driver returned %s, expected UTC wall clock; refusing to reinterpret",
				field, t.Location(),
			)
		}
		year, month, day := t.Date()
		hour, minute, sec := t.Clock()
		return time.Date(year, month, day, hour, minute, sec, t.Nanosecond(), tc.Timezone).UTC(), nil
	case nil:
		return time.Time{}, fmt.Errorf("%s is null", field)
	case string:
		if t == "" || t == "0000-00-00 00:00:00" || t == "0000-00-00" {
			return time.Time{}, fmt.Errorf("%s is empty", field)
		}
		// RFC3339 carries its own explicit offset and is parsed directly; all other layouts lack an offset and use source_timezone.
		if parsed, err := time.Parse(time.RFC3339, t); err == nil {
			// Keep branch semantics equivalent: '0001-01-01T00:00:00Z' parses to time.Time's zero value,
			// and '0000-01-15T00:00:00Z' is in year 0 AD. Both are invalid timestamps and must not be returned
			// as valid instants, or the caller's fallback branch would be bypassed.
			if parsed.IsZero() || parsed.Year() <= 0 {
				return time.Time{}, fmt.Errorf("%s is empty or year <= 0", field)
			}
			return parsed.UTC(), nil
		}
		if tc.Timezone == nil {
			return time.Time{}, fmt.Errorf("%s: source_timezone is not configured", field)
		}
		layouts := []string{"2006-01-02 15:04:05", "2006-01-02T15:04:05", "2006-01-02"}
		for _, layout := range layouts {
			if parsed, err := time.ParseInLocation(layout, t, tc.Timezone); err == nil {
				// Equivalent to the time.Time branch: year <= 0 is an invalid timestamp, not a valid instant in year 0 AD.
				if parsed.Year() <= 0 {
					return time.Time{}, fmt.Errorf("%s is empty or year <= 0", field)
				}
				return parsed.UTC(), nil
			}
		}
		// Never echo raw input: quarantine records are persisted into the migration report (legacy_migration_runs.report),
		// where the report contract mandates "no plaintext in logs or reports" and ManifestSensitiveData hardcodes
		// PlaintextInReport=false. Recording only the field name, byte length, and ASCII printability is sufficient for diagnosis.
		return time.Time{}, fmt.Errorf("%s format unsupported: %d bytes, printable=%t", field, len(t), isPrintableASCII(t))
	default:
		return time.Time{}, fmt.Errorf("%s type %T unsupported", field, v)
	}
}

// isPrintableASCII reports whether s consists solely of printable ASCII characters, providing a 1-bit
// diagnostic hint ("does this look like normal timestamp text?") in quarantine reasons without echoing raw values into persisted reports.
func isPrintableASCII(s string) bool {
	for _, r := range s {
		if r < 0x20 || r > 0x7e {
			return false
		}
	}
	return true
}

func rowString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case int64:
		return strconv.FormatInt(t, 10)
	case int:
		return strconv.Itoa(t)
	case []byte:
		return string(t)
	default:
		return fmt.Sprintf("%v", t)
	}
}

func rowInt64(v any) (int64, bool) {
	switch t := v.(type) {
	case int64:
		return t, true
	case uint64:
		if t > math.MaxInt64 {
			return 0, false
		}
		return int64(t), true
	case int:
		return int64(t), true
	case float64:
		if math.IsNaN(t) || math.IsInf(t, 0) || math.Trunc(t) != t || t < -0x1p63 || t >= 0x1p63 {
			return 0, false
		}
		return int64(t), true
	case string:
		if n, err := strconv.ParseInt(t, 10, 64); err == nil {
			return n, true
		}
	}
	return 0, false
}

// parseJSONObject strictly parses a legacy JSON column; parse failures quarantine the row per contract.
func (tc *TransformContext) parseJSONObject(table, key, field string, raw any) (map[string]any, bool) {
	text := rowString(raw)
	if text == "" {
		return map[string]any{}, true
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		tc.quarantine(table, key, fmt.Sprintf("%s json parse failed", field))
		return nil, false
	}
	return out, true
}

// allowlistFilter filters out unknown JSON keys against an allowlist and returns the sorted list of unknown keys (for quarantine).
func allowlistFilter(obj map[string]any, allowed map[string]bool) (map[string]any, []string) {
	out := make(map[string]any, len(obj))
	var unknown []string
	for k, v := range obj {
		if allowed[k] {
			out[k] = v
		} else {
			unknown = append(unknown, k)
		}
	}
	return out, unknown
}

// ensureUnknownIsError enforces the contract rule: unknown_json_key -> quarantine_and_fail_apply.
// When it returns false, the caller must quarantine the row and block Apply.
func (tc *TransformContext) ensureUnknownIsError(table, key string, unknown []string) bool {
	if len(unknown) == 0 {
		return true
	}
	tc.quarantine(table, key, fmt.Sprintf("unknown json keys: %s", strings.Join(unknown, ",")))
	return false
}

// hash64 computes a 64-character lowercase hex HMAC digest (used for char(64) columns such as identity fingerprints).
func (tc *TransformContext) hash64(domain string, data []byte) string {
	return tc.Crypter.DomainHMAC(domain, data)
}

// phash computes a "sha256:"-prefixed HMAC digest (used for candidate and Manifest fields requiring
// ^sha256:[0-9a-f]{64}$).
func (tc *TransformContext) phash(domain string, data []byte) string {
	return "sha256:" + tc.Crypter.DomainHMAC(domain, data)
}

// ---------------------------------------------------------------------------
// CoreAccount → users
// ---------------------------------------------------------------------------

// DjangoPasswordParams defines the allowlist policy for migrating legacy password hashes.
// Format: pbkdf2_sha256$<iterations>$<salt_b64>$<hash_b64>; hashes below the minimum iteration floor are treated
// as insecure and have password_hash cleared to force a password reset on first login.
const minDjangoPBKDF2Iterations = 600000

// TransformCoreAccount transforms a legacy user account; the legacy admin maps uniquely to the builtin admin.
// Returns nil when the row is quarantined (write counting is handled by the caller).
func (tc *TransformContext) TransformCoreAccount(row Row, ev *StatusEvidence) *UserModel {
	id := rowString(row["id"])
	username := strings.TrimSpace(rowString(row["username"]))
	if username == "" {
		tc.quarantine("core_accounts", id, "empty username")
		return nil
	}
	if _, exists := tc.Users[strings.ToLower(username)]; exists {
		tc.quarantine("core_accounts", id, "duplicate username after trim/case fold")
		return nil
	}
	passwordHash := rowString(row["password"])
	var hashPtr *string
	if passwordHash != "" && djangoHashAllowlisted(passwordHash) {
		hashPtr = &passwordHash
	}
	// Hash parameters below policy threshold: leave hashPtr nil so the user must reset their password on first login (not treated as a failure).
	realName := strings.TrimSpace(rowString(row["real_name"]))
	if realName == "" {
		realName = username
	}
	email := strings.TrimSpace(rowString(row["email"]))
	var emailPtr *string
	if email != "" {
		if len(email) > 320 {
			tc.quarantine("core_accounts", id, "email exceeds 320 chars")
			return nil
		}
		emailPtr = &email
	}
	uid := tc.detID("core_accounts", id)
	isAdmin := username == "admin"
	if isAdmin && tc.ExistingBuiltinAdmin != uuid.Nil {
		// Contract rule: the single legacy admin maps to the builtin admin (reusing the row bootstrapped earlier if present).
		tc.Users["admin"] = tc.ExistingBuiltinAdmin
		tc.Usernames[tc.ExistingBuiltinAdmin] = "admin"
		tc.addUserRoleObservation(username, rowString(row["is_recorder"]))
		tc.addUserLegacyMetadata(username, rowString(row["department"]))
		tc.AdminMergedToExisting = true
		return nil
	}
	tc.Users[strings.ToLower(username)] = uid
	tc.Usernames[uid] = username
	tc.addUserRoleObservation(username, rowString(row["is_recorder"]))
	tc.addUserLegacyMetadata(username, rowString(row["department"]))
	role := "user"
	if isAdmin {
		role = "admin"
	}
	return &UserModel{
		ID: uid.String(), Username: username, PasswordHash: hashPtr,
		DisplayName: realName, Email: emailPtr, IsBuiltinAdmin: isAdmin,
		Role:      role,
		CreatedAt: tc.Now, UpdatedAt: tc.Now,
	}
}

func strList(v any) []string {
	switch t := v.(type) {
	case []string:
		return t
	case nil:
		return nil
	default:
		return nil
	}
}

// addUserRoleObservation records the legacy IsRecorder observation (never grants admin or workflow privileges).
func (tc *TransformContext) addUserRoleObservation(username, isRecorder string) {
	list, _ := tc.Report["user_role_observation"].([]string)
	tc.Report["user_role_observation"] = append(list,
		fmt.Sprintf("is_recorder=%s username=%s", isRecorder, tc.hash64("sample", []byte(username))))
}

// addUserLegacyMetadata preserves non-authoritative legacy attributes such as Department.
func (tc *TransformContext) addUserLegacyMetadata(username, department string) {
	meta, _ := tc.Report["user_legacy_metadata"].(map[string]any)
	if meta == nil {
		meta = map[string]any{}
	}
	meta[tc.hash64("sample", []byte(username))] = department
	tc.Report["user_legacy_metadata"] = meta
}

// djangoHashAllowlisted checks whether a legacy Django PBKDF2 password hash satisfies the allowlist policy.
func djangoHashAllowlisted(hash string) bool {
	parts := strings.Split(hash, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2_sha256" {
		return false
	}
	iters, err := strconv.Atoi(parts[1])
	if err != nil || iters < minDjangoPBKDF2Iterations {
		return false
	}
	return len(parts[2]) >= 8 && len(parts[3]) >= 16
}

// MakeDjangoHash generates an allowlist-compliant PBKDF2-SHA256 password hash string (for builtin admin bootstrap and tests).
func MakeDjangoHash(password string, iterations int) (string, error) {
	if iterations < minDjangoPBKDF2Iterations {
		return "", fmt.Errorf("iterations below allowlist")
	}
	salt, err := randomHex(16)
	if err != nil {
		return "", err
	}
	dk := pbkdf2Key([]byte(password), []byte(salt), iterations, 32)
	return fmt.Sprintf("pbkdf2_sha256$%d$%s$%s", iterations, salt, encodeHex(dk)), nil
}

// VerifyDjangoHash verifies a password against a Django PBKDF2-SHA256 hash (used by migration-mode login and verification sampling).
// SEC-0043: hashes with an iteration count below the allowlist floor (600k) are rejected immediately --
// preventing online password guessing against weak legacy hashes during the migration window (fail closed;
// weak hashes must be reset via the bootstrap flow rather than accepted).
func VerifyDjangoHash(password, stored string) bool {
	parts := strings.Split(stored, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2_sha256" {
		return false
	}
	iters, err := strconv.Atoi(parts[1])
	if err != nil || iters < minDjangoPBKDF2Iterations {
		return false
	}
	dk := pbkdf2Key([]byte(password), []byte(parts[2]), iters, 32)
	return hmacEqual(encodeHex(dk), parts[3])
}
