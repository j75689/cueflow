package crew

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	// PageChars is how much of a long text a paging tool (read_file, get_deliverable) returns at
	// once. With the head and the status line it stays under maxToolResult, so a page is never
	// clipped.
	PageChars = 30000
	// pageHeadMax caps the head of a page (a title, a review note...): a longer head takes room from
	// the page, and past this much it is clipped so the text still gets most of the room.
	pageHeadMax = 8000
	// pageStatusMax is room for the status line ("characters a–b of n. Call ... for the rest.").
	pageStatusMax = 200
)

// PageText is one page of a tool result over long text: head, then "characters a–b of n." (and
// how to get the rest), a blank line and the page itself, from offset (a character position) for
// up to limit characters (PageChars when 0 or more). The page shrinks if need be so the whole
// result fits in what the model sees. text must be valid UTF-8; tool names the tool to call again
// and what the text, for the error when offset is past its end.
func PageText(head string, text []byte, offset, limit int, tool, what string) (string, error) {
	if offset < 0 {
		return "", fmt.Errorf("offset can't be negative")
	}
	if limit <= 0 || limit > PageChars {
		limit = PageChars
	}
	total := utf8.RuneCount(text)
	if offset > 0 && offset >= total {
		return "", fmt.Errorf("offset %d is past the end of %s, which has %d characters", offset, what, total)
	}
	head = clipNoted(head, pageHeadMax)
	limit = min(limit, maxToolResult-utf8.RuneCountInString(head)-pageStatusMax)
	end := min(offset+limit, total)
	var b strings.Builder
	fmt.Fprintf(&b, "%scharacters %d–%d of %d.", head, offset, end, total)
	if end < total {
		fmt.Fprintf(&b, " Call %s with offset %d for the rest.", tool, end)
	}
	b.WriteString("\n\n")
	b.Write(runeSlice(text, offset, end))
	return b.String(), nil
}
