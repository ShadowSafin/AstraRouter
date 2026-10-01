package domain

// MatchModelPattern reports whether a client-requested model name matches a
// policy pattern.
//
// Supported forms, in order of precedence:
//
//	""      -> matches everything (an unset pattern is a wildcard)
//	"*"     -> matches everything
//	"exact" -> literal comparison
//	"gpt-*" -> shell-style glob where '*' matches any run of characters and
//	           '?' matches exactly one character
//
// A dedicated matcher is used rather than path.Match or filepath.Match because
// both treat the path separator specially, and model identifiers such as
// "meta-llama/Llama-3-8B-Instruct" legitimately contain slashes that a pattern
// like "meta-llama/*" must be able to span.
func MatchModelPattern(pattern, name string) bool {
	switch pattern {
	case "", "*":
		return true
	}
	if pattern == name {
		return true
	}
	if !hasWildcard(pattern) {
		return false
	}
	return wildcardMatch(pattern, name)
}

func hasWildcard(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == '*' || s[i] == '?' {
			return true
		}
	}
	return false
}

// wildcardMatch performs iterative glob matching with backtracking on the last
// '*' seen. It is O(len(pattern)*len(name)) worst case and allocation-free.
func wildcardMatch(pattern, name string) bool {
	var (
		p, n         = 0, 0
		starP, starN = -1, 0
	)
	for n < len(name) {
		switch {
		case p < len(pattern) && (pattern[p] == '?' || pattern[p] == name[n]):
			p++
			n++
		case p < len(pattern) && pattern[p] == '*':
			// Remember the star position so a mismatch can retry with the star
			// consuming one more character.
			starP = p
			starN = n
			p++
		case starP >= 0:
			p = starP + 1
			starN++
			n = starN
		default:
			return false
		}
	}
	for p < len(pattern) && pattern[p] == '*' {
		p++
	}
	return p == len(pattern)
}

// MatchAnyModelPattern reports whether name matches any of the patterns. An
// empty pattern list matches everything, which keeps "unset is a wildcard"
// consistent between the single- and multi-pattern forms.
func MatchAnyModelPattern(patterns []string, name string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, p := range patterns {
		if MatchModelPattern(p, name) {
			return true
		}
	}
	return false
}
