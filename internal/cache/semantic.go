package cache

import (
	"sort"
	"strings"
)

// embed builds a normalized word-bag vector for semantic lookup. It is
// deliberately deterministic (hashed word counts, no ML model) so the same
// prompt always maps to the same neighbourhood and no model sits on the hot
// path. True embedding-assisted lookup can replace this function without
// changing the surrounding index or threshold contract.
func embed(in KeyInput) map[string]float64 {
	counts := map[string]float64{}
	add := func(s string) {
		for _, w := range strings.Fields(normalize(s)) {
			if w == "" {
				continue
			}
			counts[w]++
		}
	}
	for _, m := range in.Messages {
		add(m.Text())
	}
	return counts
}

func embeddingHash(emb map[string]float64) string {
	keys := make([]string, 0, len(emb))
	for k := range emb {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := newHash()
	for _, k := range keys {
		h.write(k)
		h.sep()
	}
	return h.sum32()
}

func cosine(a, b map[string]float64) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	dot := 0.0
	na := 0.0
	nb := 0.0
	for k, va := range a {
		na += va * va
		if vb, ok := b[k]; ok {
			dot += va * vb
		}
	}
	for _, vb := range b {
		nb += vb * vb
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (sqrt(na) * sqrt(nb))
}
