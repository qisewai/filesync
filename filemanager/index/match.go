package index

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

var ErrQuery = errors.New("无效的搜索表达式")

type globToken struct {
	char     rune
	wildcard bool
}
type nameMatcher struct {
	terms []string
	globs [][]globToken
	regex *regexp.Regexp
}

// ValidateQuery shares syntax validation between UI and backends.
func ValidateQuery(text string, regex bool) error { _, err := compileMatcher(text, regex); return err }
func compileMatcher(text string, regex bool) (*nameMatcher, error) {
	if len(text) > 4096 {
		return nil, fmt.Errorf("%w：最多 4096 字节", ErrQuery)
	}
	m := &nameMatcher{}
	if regex {
		r, err := regexp.Compile("(?i:" + text + ")")
		if err != nil {
			return nil, fmt.Errorf("%w：%v", ErrQuery, err)
		}
		m.regex = r
		return m, nil
	}
	for _, term := range strings.Fields(strings.ToLower(text)) {
		rs := []rune(term)
		var tokens []globToken
		var literal strings.Builder
		wild := false
		for i := 0; i < len(rs); i++ {
			r := rs[i]
			if r == '\\' && i+1 < len(rs) && strings.ContainsRune("*?\\", rs[i+1]) {
				i++
				r = rs[i]
				tokens = append(tokens, globToken{char: r})
				literal.WriteRune(r)
				continue
			}
			w := r == '*' || r == '?'
			wild = wild || w
			tokens = append(tokens, globToken{char: r, wildcard: w})
			literal.WriteRune(r)
		}
		if wild {
			m.globs = append(m.globs, tokens)
			var run strings.Builder
			best := ""
			for _, t := range tokens {
				if t.wildcard {
					if run.Len() > len(best) {
						best = run.String()
					}
					run.Reset()
				} else {
					run.WriteRune(t.char)
				}
			}
			if run.Len() > len(best) {
				best = run.String()
			}
			if best != "" {
				m.terms = append(m.terms, best)
			}
		} else {
			m.terms = append(m.terms, literal.String())
		}
	}
	return m, nil
}

// globMatch consumes Unicode characters without allocating a rune slice per
// filename. Only * and ? are special; brackets and dots remain literal.
func globMatch(tokens []globToken, name []byte) bool {
	p, n, star, retry := 0, 0, -1, 0
	for n < len(name) {
		if p < len(tokens) && tokens[p].wildcard && tokens[p].char == '*' {
			star = p
			p++
			retry = n
			continue
		}
		r, width := utf8.DecodeRune(name[n:])
		if p < len(tokens) && ((tokens[p].wildcard && tokens[p].char == '?') || (!tokens[p].wildcard && tokens[p].char == r)) {
			p++
			n += width
			continue
		}
		if star < 0 || retry >= len(name) {
			return false
		}
		_, width = utf8.DecodeRune(name[retry:])
		retry += width
		n = retry
		p = star + 1
	}
	for p < len(tokens) && tokens[p].wildcard && tokens[p].char == '*' {
		p++
	}
	return p == len(tokens)
}
func (m *nameMatcher) matchPatterns(lower, original []byte) bool {
	if m.regex != nil && !m.regex.Match(original) {
		return false
	}
	for _, g := range m.globs {
		if !globMatch(g, lower) {
			return false
		}
	}
	return true
}
