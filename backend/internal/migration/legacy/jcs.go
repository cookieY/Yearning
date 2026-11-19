// Package legacy implements v3 -> v4 data migration: reads the legacy MySQL metadata database,
// transforms records field by field per frozen field contracts, and writes them to a clean PostgreSQL target.
// All rules are governed by the machine contracts under api/migration/*.
package legacy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"unicode/utf8"
)

// JCSSerialize serializes a value into canonical JSON text per RFC 8785.
// Input is restricted to the JSON subset constructed by this package: string object keys, null/bool/string/int64 primitives,
// arrays, and objects; floating-point numbers are rejected (migration hash structures never produce floats).
func JCSSerialize(v any) (string, error) {
	var out []byte
	buf, err := appendJCSValue(out, v)
	if err != nil {
		return "", err
	}
	return string(buf), nil
}

// JCSHash canonicalizes v per RFC 8785 and returns its SHA-256 digest with a "sha256:" prefix.
func JCSHash(v any) (string, error) {
	text, err := JCSSerialize(v)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(text))
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// JCSHashRaw computes a "sha256:"-prefixed SHA-256 digest over already canonicalized bytes.
func JCSHashRaw(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func appendJCSValue(dst []byte, v any) ([]byte, error) {
	switch t := v.(type) {
	case nil:
		return append(dst, "null"...), nil
	case bool:
		if t {
			return append(dst, "true"...), nil
		}
		return append(dst, "false"...), nil
	case string:
		return appendJCSString(dst, t), nil
	case int:
		return appendJCSInt(dst, int64(t))
	case int64:
		return appendJCSInt(dst, t)
	case uint64:
		if t > 1<<53 {
			return nil, fmt.Errorf("jcs: integer %d exceeds safe range", t)
		}
		return appendJCSInt(dst, int64(t))
	case []any:
		dst = append(dst, '[')
		for i, item := range t {
			if i > 0 {
				dst = append(dst, ',')
			}
			var err error
			dst, err = appendJCSValue(dst, item)
			if err != nil {
				return nil, err
			}
		}
		return append(dst, ']'), nil
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys) // RFC 8785 sorts keys by UTF-16 code units; all keys in this package are ASCII
		dst = append(dst, '{')
		for i, k := range keys {
			if i > 0 {
				dst = append(dst, ',')
			}
			dst = appendJCSString(dst, k)
			dst = append(dst, ':')
			var err error
			dst, err = appendJCSValue(dst, t[k])
			if err != nil {
				return nil, err
			}
		}
		return append(dst, '}'), nil
	default:
		return nil, fmt.Errorf("jcs: unsupported type %T", v)
	}
}

func appendJCSInt(dst []byte, v int64) ([]byte, error) {
	if v < -(1<<53) || v > 1<<53 {
		return nil, fmt.Errorf("jcs: integer %d exceeds safe range", v)
	}
	return append(dst, strconv.FormatInt(v, 10)...), nil
}

func appendJCSString(dst []byte, s string) []byte {
	dst = append(dst, '"')
	for _, r := range s {
		switch r {
		case '"':
			dst = append(dst, '\\', '"')
		case '\\':
			dst = append(dst, '\\', '\\')
		case '\b':
			dst = append(dst, '\\', 'b')
		case '\f':
			dst = append(dst, '\\', 'f')
		case '\n':
			dst = append(dst, '\\', 'n')
		case '\r':
			dst = append(dst, '\\', 'r')
		case '\t':
			dst = append(dst, '\\', 't')
		default:
			if r < 0x20 || r == 0x7f {
				dst = append(dst, []byte(fmt.Sprintf(`\u%04x`, r))...)
			} else {
				dst = utf8.AppendRune(dst, r)
			}
		}
	}
	return append(dst, '"')
}

// JSONToJCSValue converts an any decoded by encoding/json into JCS-compatible types,
// rejecting float64 values to guarantee hash determinism.
func JSONToJCSValue(v any) (any, error) {
	switch t := v.(type) {
	case nil, bool, string, int64:
		return t, nil
	case int:
		return int64(t), nil
	case uint64:
		if t > 1<<53 {
			return nil, fmt.Errorf("jcs: integer %d exceeds safe range", t)
		}
		return int64(t), nil
	case float64:
		if t == float64(int64(t)) && abs64(t) <= 1<<53 {
			return int64(t), nil
		}
		return nil, fmt.Errorf("jcs: non-integral number %v not allowed in hashed JSON", t)
	case json.Number:
		i, err := t.Int64()
		if err != nil {
			return nil, fmt.Errorf("jcs: json.Number %s is not an integer", t.String())
		}
		return i, nil
	case []any:
		out := make([]any, 0, len(t))
		for _, item := range t {
			c, err := JSONToJCSValue(item)
			if err != nil {
				return nil, err
			}
			out = append(out, c)
		}
		return out, nil
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, item := range t {
			c, err := JSONToJCSValue(item)
			if err != nil {
				return nil, err
			}
			out[k] = c
		}
		return out, nil
	default:
		return nil, fmt.Errorf("jcs: unsupported decoded type %T", v)
	}
}

func abs64(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}
