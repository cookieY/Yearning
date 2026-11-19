// Package schema implements the versioned migration executor for db/migrations and the schema_migrations ledger.
// Ledger rules (see db/migrations/README.md): one transaction per migration file, SHA-256 checksum verification,
// persistent failure records, and no advisory locks (concurrent migrators fail naturally on primary key conflict).
package schema

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Migration represents a single .up.sql file in the migrations directory.
type Migration struct {
	Version    int64
	Filename   string
	Path       string
	SHA256     string
	Statements []string
}

var filenamePattern = regexp.MustCompile(`^(\d{6})_[a-z0-9_]+\.up\.sql$`)

// LoadDir loads all *.up.sql files in the directory in ascending version order and computes their SHA-256 checksums.
// Returns an error if any file name violates the contracted naming pattern so unknown files are never silently skipped.
func LoadDir(dir string) ([]Migration, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read migrations dir: %w", err)
	}
	var out []Migration
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".up.sql") {
			// Documentation files such as README.md are permitted in the directory; any other non-migration file returns an error.
			if strings.HasPrefix(strings.ToLower(name), "readme") {
				continue
			}
			return nil, fmt.Errorf("unexpected non-up file in migrations dir: %s", name)
		}
		m := filenamePattern.FindStringSubmatch(name)
		if m == nil {
			return nil, fmt.Errorf("migration filename violates contract: %s", name)
		}
		version, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse migration version: %w", err)
		}
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, fmt.Errorf("read migration file: %w", err)
		}
		sum := sha256.Sum256(raw)
		out = append(out, Migration{
			Version:    version,
			Filename:   name,
			Path:       filepath.Join(dir, name),
			SHA256:     hex.EncodeToString(sum[:]),
			Statements: SplitStatements(string(raw)),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	for i, m := range out {
		if m.Version != int64(i+1) {
			return nil, fmt.Errorf("migration versions must be contiguous from 1, got gap at %06d", m.Version)
		}
	}
	return out, nil
}

// SplitStatements splits SQL text on top-level semicolons while properly skipping single-quoted strings,
// double-quoted identifiers, line comments, block comments, and dollar-quoted bodies.
func SplitStatements(sqlText string) []string {
	var (
		out        []string
		current    strings.Builder
		inSingle   bool
		inDouble   bool
		inLineCmt  bool
		inBlockCmt bool
		dollarTag  string
	)
	flush := func() {
		stmt := strings.TrimSpace(current.String())
		if hasExecutableContent(stmt) {
			out = append(out, stmt)
		}
		current.Reset()
	}
	runes := []rune(sqlText)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if inLineCmt {
			if r == '\n' {
				inLineCmt = false
			}
			current.WriteRune(r)
			continue
		}
		if inBlockCmt {
			if r == '*' && i+1 < len(runes) && runes[i+1] == '/' {
				inBlockCmt = false
				current.WriteString("*/")
				i++
			} else {
				current.WriteRune(r)
			}
			continue
		}
		if dollarTag != "" {
			if strings.HasPrefix(string(runes[i:]), dollarTag) {
				current.WriteString(dollarTag)
				i += len(dollarTag) - 1
				dollarTag = ""
				continue
			}
			current.WriteRune(r)
			continue
		}
		if inSingle {
			if r == '\'' && i+1 < len(runes) && runes[i+1] == '\'' {
				current.WriteString("''")
				i++
				continue
			}
			if r == '\'' {
				inSingle = false
			}
			current.WriteRune(r)
			continue
		}
		if inDouble {
			if r == '"' && i+1 < len(runes) && runes[i+1] == '"' {
				current.WriteString(`""`)
				i++
				continue
			}
			if r == '"' {
				inDouble = false
			}
			current.WriteRune(r)
			continue
		}
		switch {
		case r == '-' && i+1 < len(runes) && runes[i+1] == '-':
			inLineCmt = true
			current.WriteRune(r)
		case r == '/' && i+1 < len(runes) && runes[i+1] == '*':
			inBlockCmt = true
			current.WriteRune(r)
		case r == '\'':
			inSingle = true
			current.WriteRune(r)
		case r == '"':
			inDouble = true
			current.WriteRune(r)
		case r == '$':
			if tag := matchDollarTag(string(runes[i:])); tag != "" {
				dollarTag = tag
				current.WriteString(tag)
				i += len(tag) - 1
				continue
			}
			current.WriteRune(r)
		case r == ';':
			flush()
		default:
			current.WriteRune(r)
		}
	}
	flush()
	return out
}

// Precompile regex: SplitStatements is called frequently during directory loading and statement splitting,
// avoiding repeated regexp.Compile overhead.
var (
	dollarTagPattern  = regexp.MustCompile(`^\$[A-Za-z_0-9]*\$`)
	lineCommentRegex  = regexp.MustCompile(`(?m)--[^\n]*`)
	blockCommentRegex = regexp.MustCompile(`(?s)/\*.*?\*/`)
)

func matchDollarTag(rest string) string {
	// Dollar-quote tags take the form $$ or $tag$ and must appear in matching pairs.
	m := dollarTagPattern.FindString(rest)
	if m == "" {
		return ""
	}
	if !strings.Contains(rest[len(m):], m) {
		return ""
	}
	return m
}

// hasExecutableContent reports whether SQL text still contains executable tokens after stripping comments.
func hasExecutableContent(stmt string) bool {
	s := lineCommentRegex.ReplaceAllString(stmt, "")
	s = blockCommentRegex.ReplaceAllString(s, "")
	return strings.TrimSpace(s) != ""
}
