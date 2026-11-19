// Package fingerprint implements multi-dialect SQL dual fingerprinting, typed literal normalization, and anonymous template generation.
//
// Security invariants (sql-fingerprint.json):
//   - Output quality is strictly either exact (dialect-aware role classification succeeded) or compatible_lexer
//     (lexical fallback that provably covers statement boundaries, literals, identifiers, and comment removal);
//   - Regular expressions are not used for implementation; unknown tokens are not comments; dropping unknown tokens is forbidden;
//   - Fails closed with a structured error whenever the lexer cannot prove boundaries, literals, identifiers, or encoding;
//   - All three HMAC types use the sql_fingerprint purpose key and output lowercase hex;
//   - anonymized_sql must pass privacy.AssertNoLeak before returning.
package fingerprint

import (
	"fmt"
	"strings"
)

// AlgorithmVersion matches contract algorithm_version; upgrades must increment the version string.
const AlgorithmVersion = "yearning-fingerprint-v1"

// Profile identifies a dialect profile: engine/mode (e.g. mysql/mysql, tidb/mysql, polardb/postgresql).
type Profile struct {
	Engine string
	Mode   string
}

func (p Profile) String() string { return p.Engine + "/" + p.Mode }

// supportedProfiles is a frozen copy of sql-fingerprint.json supported_profiles
// (6 profiles in total: mysql, postgresql, tidb, oceanbase, polardb/mysql,
// polardb/postgresql; cloud vendor versions are frozen per release as latest_stable_at_release).
var supportedProfiles = map[Profile]bool{
	{"mysql", "mysql"}:           true,
	{"postgresql", "postgresql"}: true,
	{"tidb", "mysql"}:            true,
	{"oceanbase", "mysql"}:       true,
	{"polardb", "mysql"}:         true,
	{"polardb", "postgresql"}:    true,
}

// DialectFlags describes lexical differences across dialect families.
type DialectFlags struct {
	// BacktickIdent: MySQL family — backticks quote identifiers.
	BacktickIdent bool
	// DoubleQuoteIdent: PG family — double quotes quote identifiers (in the MySQL family, double quotes denote strings).
	DoubleQuoteIdent bool
	// BackslashEscape: MySQL family — backslash escaping inside strings. Session sql_mode is fixed by
	// database-adapter-contract.json (no NO_BACKSLASH_ESCAPES).
	BackslashEscape bool
	// HashLineComment: MySQL family — # line comments.
	HashLineComment bool
	// ExecutableVersionComment: MySQL family — /*!12345 ...*/ contains executable SQL.
	ExecutableVersionComment bool
	// DollarQuote: PG family — $tag$...$tag$ strings.
	DollarQuote bool
	// DollarParam: PG family — $1 positional parameters.
	DollarParam bool
	// NestedBlockComment: PG family — /* /* */ */ nested block comments.
	NestedBlockComment bool
}

// mysqlFlags / postgresFlags define baseline dialect-family behavior; vendor differences enter the
// dialect fingerprint via the profile name, while the lexer shares family-level behavior.
var mysqlFlags = DialectFlags{
	BacktickIdent:            true,
	DoubleQuoteIdent:         false,
	BackslashEscape:          true,
	HashLineComment:          true,
	ExecutableVersionComment: true,
}

var postgresFlags = DialectFlags{
	BacktickIdent:      false,
	DoubleQuoteIdent:   true,
	BackslashEscape:    false,
	DollarQuote:        true,
	DollarParam:        true,
	NestedBlockComment: true,
}

// Validate checks whether the profile is in the support matrix; unknown names fail closed.
func (p Profile) Validate() error {
	if !supportedProfiles[p] {
		return fmt.Errorf("fingerprint: dialect_profile_unknown: %s", p)
	}
	return nil
}

// Flags returns the dialect-family lexical behavior for this profile.
func (p Profile) Flags() DialectFlags {
	if p.Mode == "postgresql" {
		return postgresFlags
	}
	return mysqlFlags
}

// IsPostgres reports whether the profile belongs to the PG dialect family.
func (p Profile) IsPostgres() bool { return p.Mode == "postgresql" }

// ParseProfile parses an "engine/mode" string.
func ParseProfile(s string) (Profile, error) {
	parts := strings.SplitN(s, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return Profile{}, fmt.Errorf("fingerprint: malformed dialect profile %q", s)
	}
	return Profile{Engine: parts[0], Mode: parts[1]}, nil
}
