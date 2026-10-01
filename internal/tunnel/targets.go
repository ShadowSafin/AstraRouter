package tunnel

import (
	"strconv"
	"strings"
)

// splitHostPort splits a "host:port" target without accepting URLs, paths,
// schemes or userinfo. Bracketed IPv6 literals ([::1]:8080) are supported;
// unbracketed colons are rejected rather than guessed at.
func splitHostPort(target string) (host, port string, ok bool) {
	if target == "" || len(target) > 253 {
		return "", "", false
	}
	for _, r := range target {
		if r <= ' ' || r == '/' || r == '?' || r == '#' || r == '@' {
			return "", "", false
		}
	}
	if strings.HasPrefix(target, "[") {
		end := strings.Index(target, "]")
		if end < 0 || end+1 >= len(target) || target[end+1] != ':' {
			return "", "", false
		}
		return target[1:end], target[end+2:], target[end+2:] != ""
	}
	idx := strings.LastIndex(target, ":")
	if idx <= 0 || idx+1 >= len(target) {
		return "", "", false
	}
	host, port = target[:idx], target[idx+1:]
	if strings.Contains(host, ":") {
		return "", "", false
	}
	return host, port, true
}

// isLoopbackHost reports whether a target host is this machine. Only loopback
// destinations may be exposed: a tunnel must never turn CoreRouter into a
// proxy for infrastructure it does not own.
func isLoopbackHost(host string) bool {
	switch strings.ToLower(strings.TrimSuffix(host, ".")) {
	case "localhost", "127.0.0.1", "::1":
		return true
	default:
		return false
	}
}

// validPort reports whether a port string is a usable TCP port.
func validPort(port string) bool {
	n, err := strconv.Atoi(port)
	if err != nil {
		return false
	}
	return n >= 1 && n <= 65535
}
