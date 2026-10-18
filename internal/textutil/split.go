// Package textutil contains text helpers: thread splitting and HTML rendering for publishers.
package textutil

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// SplitThread breaks text into parts of at most limit runes, preferring paragraph, then sentence,
// then word boundaries. Already-short text returns a single part.
func SplitThread(text string, limit int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	if limit <= 0 || utf8.RuneCountInString(text) <= limit {
		return []string{text}
	}
	var units []unit
	for _, para := range splitParagraphs(text) {
		first := true
		add := func(t string) {
			units = append(units, unit{text: t, newPara: first})
			first = false
		}
		if utf8.RuneCountInString(para) <= limit {
			add(para)
			continue
		}
		for _, s := range splitSentences(para) {
			if utf8.RuneCountInString(s) <= limit {
				add(s)
				continue
			}
			for _, w := range hardWrap(s, limit) {
				add(w)
			}
		}
	}
	return pack(units, limit)
}

type unit struct {
	text    string
	newPara bool // first unit of a paragraph
}

// FitParts re-splits any part longer than limit, keeping the author's own breaks otherwise.
func FitParts(parts []string, limit int) []string {
	var out []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, SplitThread(p, limit)...)
	}
	return out
}

func splitParagraphs(s string) []string {
	var out []string
	for _, p := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n\n") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// splitSentences splits after sentence terminators (Latin and CJK), keeping the terminator.
func splitSentences(s string) []string {
	var out []string
	var b strings.Builder
	rs := []rune(s)
	for i, r := range rs {
		b.WriteRune(r)
		if isTerminator(r) {
			next := rune(0)
			if i+1 < len(rs) {
				next = rs[i+1]
			}
			// CJK terminators end a sentence on their own; Latin ones need following space/end.
			if isCJKTerminator(r) || next == 0 || unicode.IsSpace(next) {
				if t := strings.TrimSpace(b.String()); t != "" {
					out = append(out, t)
				}
				b.Reset()
			}
		} else if r == '\n' {
			if t := strings.TrimSpace(b.String()); t != "" {
				out = append(out, t)
			}
			b.Reset()
		}
	}
	if t := strings.TrimSpace(b.String()); t != "" {
		out = append(out, t)
	}
	return out
}

func isTerminator(r rune) bool {
	return r == '.' || r == '!' || r == '?' || isCJKTerminator(r)
}

func isCJKTerminator(r rune) bool {
	// Ideographic full stop, fullwidth exclamation/question marks, fullwidth semicolon.
	return r == '\u3002' || r == '\uff01' || r == '\uff1f' || r == '\uff1b'
}

// hardWrap cuts at the last space before the limit, or exactly at the limit for unspaced text.
func hardWrap(s string, limit int) []string {
	var out []string
	rs := []rune(s)
	for len(rs) > limit {
		cut := limit
		for i := limit; i > limit/2; i-- {
			if unicode.IsSpace(rs[i]) {
				cut = i
				break
			}
		}
		out = append(out, strings.TrimSpace(string(rs[:cut])))
		rs = []rune(strings.TrimSpace(string(rs[cut:])))
	}
	if len(rs) > 0 {
		out = append(out, string(rs))
	}
	return out
}

// pack greedily joins units into parts no longer than limit. Paragraph boundaries become blank
// lines; sentences after CJK punctuation are joined without a space.
func pack(units []unit, limit int) []string {
	var out []string
	cur := ""
	for _, u := range units {
		if cur == "" {
			cur = u.text
			continue
		}
		sep := " "
		if u.newPara {
			sep = "\n\n"
		} else if r, _ := utf8.DecodeLastRuneInString(cur); isCJKTerminator(r) {
			sep = ""
		}
		if utf8.RuneCountInString(cur)+utf8.RuneCountInString(sep)+utf8.RuneCountInString(u.text) <= limit {
			cur += sep + u.text
		} else {
			out = append(out, cur)
			cur = u.text
		}
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}
