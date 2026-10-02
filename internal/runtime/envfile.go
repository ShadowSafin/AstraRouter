package runtime

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"strings"
)

// ParseEnvFile parses KEY=VALUE files in the docker-compose env_file dialect:
//
//   - blank lines and full-line `#` comments are ignored
//   - a leading `export ` is accepted and stripped
//   - values may be unquoted or wrapped in single or double quotes; double
//     quotes honour \\, \n, \t and \" escapes
//   - inline comments are not special: a `#` inside a value is data, which is
//     what keeps generated secrets and URLs intact
//
// Anything else is a line-numbered error, because a silently misread secret is
// worse than a loud failure at startup.
func ParseEnvFile(data []byte) (map[string]string, error) {
	out := map[string]string{}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	// Long values (certificates, JSON blobs) exceed the default buffer.
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if rest, ok := strings.CutPrefix(line, "export "); ok {
			line = strings.TrimSpace(rest)
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("line %d: expected KEY=VALUE", lineNo)
		}
		key = strings.TrimSpace(key)
		if key == "" {
			return nil, fmt.Errorf("line %d: empty variable name", lineNo)
		}
		for _, r := range key {
			if r != '_' && (r < '0' || r > '9') && (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') {
				return nil, fmt.Errorf("line %d: invalid variable name %q", lineNo, key)
			}
		}
		parsed, err := parseEnvValue(strings.TrimSpace(value))
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNo, err)
		}
		out[key] = parsed
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read env data: %w", err)
	}
	return out, nil
}

// parseEnvValue unquotes a single value.
func parseEnvValue(raw string) (string, error) {
	if len(raw) >= 2 && raw[0] == '\'' && raw[len(raw)-1] == '\'' {
		return raw[1 : len(raw)-1], nil
	}
	if len(raw) >= 2 && raw[0] == '"' && raw[len(raw)-1] == '"' {
		inner := raw[1 : len(raw)-1]
		var sb strings.Builder
		escaped := false
		for _, r := range inner {
			if escaped {
				switch r {
				case 'n':
					sb.WriteByte('\n')
				case 't':
					sb.WriteByte('\t')
				case '\\', '"':
					sb.WriteRune(r)
				default:
					sb.WriteByte('\\')
					sb.WriteRune(r)
				}
				escaped = false
				continue
			}
			if r == '\\' {
				escaped = true
				continue
			}
			sb.WriteRune(r)
		}
		if escaped {
			sb.WriteByte('\\')
		}
		return sb.String(), nil
	}
	if strings.ContainsAny(raw, "'\"") {
		return "", fmt.Errorf("unbalanced quote in %q", raw)
	}
	return raw, nil
}

// LoadEnvFile reads path and returns its variables without touching the
// process environment, so callers can inspect or merge first.
func LoadEnvFile(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseEnvFile(data)
}

// ApplyEnvDefaults sets each variable that is not already present in the
// process environment. The shell always wins over the file: an exported value
// is an explicit operator decision, while the file holds installation defaults.
func ApplyEnvDefaults(vars map[string]string) {
	for key, value := range vars {
		if _, present := os.LookupEnv(key); !present {
			_ = os.Setenv(key, value)
		}
	}
}
