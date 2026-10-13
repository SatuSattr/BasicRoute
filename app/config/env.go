package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type FlexInt int

func (v *FlexInt) UnmarshalJSON(data []byte) error {
	s := strings.TrimSpace(string(data))
	if s == "null" {
		return nil
	}
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		var str string
		if err := json.Unmarshal(data, &str); err != nil {
			return err
		}
		str = strings.TrimSpace(str)
		n, err := strconv.Atoi(str)
		if err != nil {
			return fmt.Errorf("flexint: %q is not a valid integer", str)
		}
		*v = FlexInt(n)
		return nil
	}
	var n int
	if err := json.Unmarshal(data, &n); err != nil {
		return fmt.Errorf("flexint: %w", err)
	}
	*v = FlexInt(n)
	return nil
}

type FlexFloat float64

func (v *FlexFloat) UnmarshalJSON(data []byte) error {
	s := strings.TrimSpace(string(data))
	if s == "null" {
		return nil
	}
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		var str string
		if err := json.Unmarshal(data, &str); err != nil {
			return err
		}
		str = strings.TrimSpace(str)
		f, err := strconv.ParseFloat(str, 64)
		if err != nil {
			return fmt.Errorf("flexfloat: %q is not a valid number", str)
		}
		*v = FlexFloat(f)
		return nil
	}
	var f float64
	if err := json.Unmarshal(data, &f); err != nil {
		return fmt.Errorf("flexfloat: %w", err)
	}
	*v = FlexFloat(f)
	return nil
}

type FlexBool bool

func (v *FlexBool) UnmarshalJSON(data []byte) error {
	s := strings.TrimSpace(string(data))
	if s == "null" {
		return nil
	}
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		var str string
		if err := json.Unmarshal(data, &str); err != nil {
			return err
		}
		switch strings.ToLower(strings.TrimSpace(str)) {
		case "true", "1":
			*v = true
			return nil
		case "false", "0":
			*v = false
			return nil
		default:
			return fmt.Errorf("flexbool: %q is not a valid boolean (want true/false/1/0)", str)
		}
	}
	var b bool
	if err := json.Unmarshal(data, &b); err != nil {
		return fmt.Errorf("flexbool: %w", err)
	}
	*v = FlexBool(b)
	return nil
}

func parseDotEnv(src string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(src, "\n") {
		line = strings.TrimRight(line, "\r")
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		if rest, ok := cutExportPrefix(t); ok {
			t = strings.TrimSpace(rest)
			if t == "" || strings.HasPrefix(t, "#") {
				continue
			}
		}
		eq := strings.IndexByte(t, '=')
		if eq < 0 {
			continue
		}
		key := strings.TrimSpace(t[:eq])
		if !validEnvName(key) {
			continue
		}
		val := strings.TrimSpace(t[eq+1:])
		out[key] = parseDotEnvValue(val)
	}
	return out
}

func cutExportPrefix(s string) (string, bool) {
	if strings.HasPrefix(s, "export ") {
		return s[len("export "):], true
	}
	if strings.HasPrefix(s, "export\t") {
		return s[len("export\t"):], true
	}
	return s, false
}

func parseDotEnvValue(v string) string {
	if len(v) >= 2 {
		if v[0] == '"' && v[len(v)-1] == '"' {
			return unescapeDoubleQuoted(v[1 : len(v)-1])
		}
		if v[0] == '\'' && v[len(v)-1] == '\'' {
			return v[1 : len(v)-1] // single-quoted: literal
		}
	}
	return cutInlineComment(v)
}

func cutInlineComment(v string) string {
	for i := 0; i < len(v); i++ {
		if v[i] == '#' && i > 0 && (v[i-1] == ' ' || v[i-1] == '\t') {
			return strings.TrimRight(v[:i-1], " \t")
		}
	}
	return v
}

func unescapeDoubleQuoted(s string) string {
	if !strings.ContainsRune(s, '\\') {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 >= len(s) {
			b.WriteByte(s[i])
			continue
		}
		switch s[i+1] {
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 't':
			b.WriteByte('\t')
		case '"':
			b.WriteByte('"')
		case '\\':
			b.WriteByte('\\')
		default:
			b.WriteByte(s[i+1])
		}
		i++
	}
	return b.String()
}

func validEnvName(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		ok := c == '_' || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (i > 0 && c >= '0' && c <= '9')
		if !ok {
			return false
		}
	}
	return true
}

func collectEnvFiles(cfgPath, envFlag string) []string {
	if envFlag == "-" {
		return nil
	}
	if envFlag != "" {
		return []string{envFlag}
	}
	var out []string
	seen := map[string]bool{}
	add := func(p string) {
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		out = append(out, p)
	}
	if dir := filepath.Dir(cfgPath); dir != "" {
		add(filepath.Join(dir, ".env"))
	}
	if cwd, err := os.Getwd(); err == nil {
		add(filepath.Join(cwd, ".env"))
	}
	return out
}

func makeLookup(extra map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		if v, ok := os.LookupEnv(k); ok {
			return v, true
		}
		if v, ok := extra[k]; ok {
			return v, true
		}
		return "", false
	}
}

const envPlaceholderPrefix = "<ENV:"

func expandEnvPlaceholders(src []byte, lookup func(string) (string, bool)) ([]byte, error) {
	var out []byte
	out = make([]byte, 0, len(src))
	inString := false
	escaped := false
	line := 1
	i := 0
	for i < len(src) {
		c := src[i]
		if c == '\n' {
			line++
		}
		if inString {
			if escaped {
				escaped = false
				out = append(out, c)
				i++
				continue
			}
			if c == '\\' {
				escaped = true
				out = append(out, c)
				i++
				continue
			}
			if c == '"' {
				inString = false
				out = append(out, c)
				i++
				continue
			}
			if hasPrefixAt(src, i, envPlaceholderPrefix) {
				val, next, err := resolvePlaceholder(src, i, line, lookup)
				if err != nil {
					return nil, err
				}
				out = appendJSONEscaped(out, val)
				i = next
				continue
			}
			out = append(out, c)
			i++
			continue
		}
		if c == '"' {
			inString = true
			out = append(out, c)
			i++
			continue
		}
		if hasPrefixAt(src, i, envPlaceholderPrefix) {
			val, next, err := resolvePlaceholder(src, i, line, lookup)
			if err != nil {
				return nil, err
			}
			out = append(out, val...)
			i = next
			continue
		}
		out = append(out, c)
		i++
	}
	if inString {
		return nil, fmt.Errorf("unterminated string during env expansion (line %d)", line)
	}
	return out, nil
}

func resolvePlaceholder(src []byte, start, line int, lookup func(string) (string, bool)) (string, int, error) {
	rest := src[start+len(envPlaceholderPrefix):]
	end := -1
	for j := 0; j < len(rest); j++ {
		if rest[j] == '>' {
			end = j
			break
		}
		if rest[j] == '\n' || rest[j] == '\r' {
			break // placeholder tidak boleh nyeberang baris
		}
	}
	if end < 0 {
		return "", 0, fmt.Errorf("invalid env placeholder (missing closing '>') on line %d", line)
	}
	inner := string(rest[:end])
	name, def, hasDef := splitPlaceholderInner(inner)
	if !validEnvName(name) {
		return "", 0, fmt.Errorf("invalid env placeholder %q on line %d (name must be [A-Za-z_][A-Za-z0-9_]*)", "<ENV:"+inner+">", line)
	}
	if v, ok := lookup(name); ok {
		return v, start + len(envPlaceholderPrefix) + end + 1, nil
	}
	if hasDef {
		return def, start + len(envPlaceholderPrefix) + end + 1, nil
	}
	return "", 0, fmt.Errorf("env %q not found (placeholder <ENV:%s> on line %d, no default)", name, name, line)
}

func splitPlaceholderInner(inner string) (name, def string, hasDef bool) {
	for i := 0; i < len(inner); i++ {
		if inner[i] == '|' {
			return strings.TrimSpace(inner[:i]), inner[i+1:], true
		}
		if inner[i] == ':' && i+1 < len(inner) && inner[i+1] == '-' {
			return strings.TrimSpace(inner[:i]), inner[i+2:], true
		}
	}
	return strings.TrimSpace(inner), "", false
}

func hasPrefixAt(src []byte, i int, prefix string) bool {
	if i+len(prefix) > len(src) {
		return false
	}
	for j := 0; j < len(prefix); j++ {
		if src[i+j] != prefix[j] {
			return false
		}
	}
	return true
}

func appendJSONEscaped(out []byte, s string) []byte {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '"':
			out = append(out, '\\', '"')
		case '\\':
			out = append(out, '\\', '\\')
		case '\n':
			out = append(out, '\\', 'n')
		case '\r':
			out = append(out, '\\', 'r')
		case '\t':
			out = append(out, '\\', 't')
		case '\b':
			out = append(out, '\\', 'b')
		case '\f':
			out = append(out, '\\', 'f')
		default:
			if s[i] < 0x20 {
				out = append(out, fmt.Sprintf(`\u%04x`, s[i])...)
			} else {
				out = append(out, s[i])
			}
		}
	}
	return out
}
