package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
)

func normalize(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func floatStr(f float64) string {
	return strings.TrimRight(strings.TrimRight(jsonNumber(f), "0"), ".")
}

func jsonNumber(f float64) string {
	b, _ := json.Marshal(f)
	return string(b)
}

func sqrt(x float64) float64 {
	if x <= 0 {
		return 0
	}
	z := x
	for i := 0; i < 20; i++ {
		z -= (z*z - x) / (2 * z)
	}
	return z
}

// hash is a tiny streaming sha256 helper for semantic hashes.
type hash struct {
	h [32]byte
	b []byte
}

func newHash() *hash { return &hash{} }

func (h *hash) write(s string) { h.b = append(h.b, s...) }
func (h *hash) sep()           { h.b = append(h.b, 0) }
func (h *hash) sum32() string {
	sum := sha256.Sum256(h.b)
	_ = h.h
	return hex.EncodeToString(sum[:])[:32]
}
