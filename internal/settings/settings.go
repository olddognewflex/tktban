// Package settings persists machine-local UI preferences for tktban as
// human-readable TOML.
//
// This is tktban UI state ONLY (theme, with room for more) — never board data,
// and never written through tkt. It lives under the standard config dir so it
// survives across runs. Loading never errors: a missing or corrupt file falls
// back to defaults so the app always starts.
//
// The TOML handling is deliberately hand-rolled and dependency-free: only flat
// scalar keys are read or written, which is all the UI prefs need, and it keeps
// the core port stdlib-only. It mirrors the Python's custom dump/escape so any
// value round-trips.
package settings

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Defaults are the known UI preferences and their default values. Extend this
// as more state is persisted; unknown keys read from disk are ignored so an
// old/newer file never breaks startup.
var Defaults = map[string]any{"theme": "textual-dark"}

// DefaultPath is $XDG_CONFIG_HOME/tktban/settings.toml, falling back to
// ~/.config/tktban/settings.toml.
func DefaultPath() string {
	root := os.Getenv("XDG_CONFIG_HOME")
	if root == "" {
		home, _ := os.UserHomeDir()
		root = filepath.Join(home, ".config")
	}
	return filepath.Join(root, "tktban", "settings.toml")
}

// Load reads settings layered over Defaults. A missing, unreadable, or corrupt
// file yields a copy of Defaults — this never errors, so a bad file can't stop
// tktban from starting. Only known keys with scalar values are taken from disk.
func Load(path string) map[string]any {
	data := make(map[string]any, len(Defaults))
	maps.Copy(data, Defaults)
	raw, err := os.ReadFile(path)
	if err != nil {
		return data
	}
	loaded, err := parseScalars(string(raw))
	if err != nil {
		return data
	}
	for k, v := range loaded {
		if _, known := Defaults[k]; known {
			data[k] = v
		}
	}
	return data
}

// Save writes settings as TOML, creating the config dir if needed. Only known
// keys are written, so transient/unknown state never leaks to disk.
func Save(path string, data map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	persisted := make(map[string]any, len(data))
	for k, v := range data {
		if _, known := Defaults[k]; known {
			persisted[k] = v
		}
	}
	return os.WriteFile(path, []byte(dumpTOML(persisted)), 0o644)
}

// ---- TOML serialization ----

// tomlEscapes are the TOML basic-string escapes (https://toml.io/en/v1.0.0#string).
// Other control chars are \u-encoded; everything else is written literally.
var tomlEscapes = map[rune]string{
	'\\': `\\`, '"': `\"`, '\b': `\b`, '\t': `\t`,
	'\n': `\n`, '\f': `\f`, '\r': `\r`,
}

func escapeStr(value string) string {
	var b strings.Builder
	for _, ch := range value {
		if esc, ok := tomlEscapes[ch]; ok {
			b.WriteString(esc)
		} else if ch < 0x20 || ch == 0x7F {
			fmt.Fprintf(&b, `\u%04x`, ch)
		} else {
			b.WriteRune(ch)
		}
	}
	return b.String()
}

// dumpTOML serializes a flat dict of scalar values to TOML. Supports
// string/bool/int/float — enough for UI prefs. Strings are fully escaped to the
// TOML basic-string spec so any value round-trips back through a parser.
func dumpTOML(data map[string]any) string {
	// Stable key order keeps output deterministic.
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var lines []string
	for _, k := range keys {
		var rendered string
		switch v := data[k].(type) {
		case bool:
			if v {
				rendered = "true"
			} else {
				rendered = "false"
			}
		case int:
			rendered = strconv.Itoa(v)
		case int64:
			rendered = strconv.FormatInt(v, 10)
		case float64:
			rendered = strconv.FormatFloat(v, 'g', -1, 64)
		case string:
			rendered = `"` + escapeStr(v) + `"`
		default:
			rendered = `"` + escapeStr(fmt.Sprint(v)) + `"`
		}
		lines = append(lines, k+" = "+rendered)
	}
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}

// parseScalars reads flat `key = value` TOML lines. It tolerates blank lines,
// `#` comments, and `[table]` headers (skipped). A line that looks like an
// assignment but whose value can't be parsed is treated as corrupt and errors,
// so Load falls back to defaults — matching the Python's all-or-defaults
// behavior on a malformed file.
func parseScalars(text string) (map[string]any, error) {
	out := map[string]any{}
	for raw := range strings.SplitSeq(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "[") {
			continue
		}
		key, valStr, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("invalid TOML line: %q", raw)
		}
		key = strings.TrimSpace(key)
		val, err := parseValue(strings.TrimSpace(valStr))
		if err != nil {
			return nil, err
		}
		out[key] = val
	}
	return out, nil
}

func parseValue(s string) (any, error) {
	if s == "" {
		return nil, fmt.Errorf("empty TOML value")
	}
	if s[0] == '"' {
		return parseBasicString(s)
	}
	if s == "true" {
		return true, nil
	}
	if s == "false" {
		return false, nil
	}
	if i, err := strconv.ParseInt(s, 10, 64); err == nil {
		return i, nil
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return f, nil
	}
	return nil, fmt.Errorf("unrecognized TOML value: %q", s)
}

func parseBasicString(s string) (string, error) {
	if len(s) < 2 || s[len(s)-1] != '"' {
		return "", fmt.Errorf("unterminated string: %q", s)
	}
	body := s[1 : len(s)-1]
	var b strings.Builder
	for i := 0; i < len(body); i++ {
		ch := body[i]
		if ch != '\\' {
			b.WriteByte(ch)
			continue
		}
		i++
		if i >= len(body) {
			return "", fmt.Errorf("trailing backslash in string")
		}
		switch body[i] {
		case '\\':
			b.WriteByte('\\')
		case '"':
			b.WriteByte('"')
		case 'b':
			b.WriteByte('\b')
		case 't':
			b.WriteByte('\t')
		case 'n':
			b.WriteByte('\n')
		case 'f':
			b.WriteByte('\f')
		case 'r':
			b.WriteByte('\r')
		case 'u':
			if i+4 >= len(body) {
				return "", fmt.Errorf("short \\u escape")
			}
			code, err := strconv.ParseUint(body[i+1:i+5], 16, 32)
			if err != nil {
				return "", fmt.Errorf("bad \\u escape: %v", err)
			}
			b.WriteRune(rune(code))
			i += 4
		default:
			return "", fmt.Errorf("unknown escape \\%c", body[i])
		}
	}
	return b.String(), nil
}
