package llm

import (
	"fmt"
	"unicode/utf8"
)

// ToolHistoryKept is what TrimToolHistory leaves of a result it cuts. Results shorter than twice
// that are left alone: cutting them saves little.
const ToolHistoryKept = 2000

// ClipNoted cuts s to n characters and, when it does, ends it with how many were shown out of how
// many, plus hint.
func ClipNoted(s string, n int, hint string) string {
	if len(s) <= n {
		return s
	}
	total := utf8.RuneCountInString(s)
	if total <= n {
		return s
	}
	end, k := len(s), 0
	for i := range s {
		if k == n {
			end = i
			break
		}
		k++
	}
	return fmt.Sprintf("%s… (truncated: showed the first %d of %d characters%s)", s[:end], n, total, hint)
}

// TrimToolHistory returns msgs with the oldest tool results cut to ToolHistoryKept characters until
// the results fit in budget. The newest turn's results stay whole. msgs itself is not changed. The
// cuts depend only on msgs, so a result cut once stays cut the same way on every later turn, and a
// result already cut is left alone.
//
// A cut changes what the model saw before, so it is only for connectors that are not a
// HistoryShedder (or a shedder's own fallback that also drops what the cut invalidates).
func TrimToolHistory(msgs []Message, budget int) []Message {
	if budget <= 0 {
		return msgs
	}
	newest := len(msgs)
	for newest > 0 && msgs[newest-1].Role == RoleTool {
		newest--
	}
	total := 0
	for _, m := range msgs {
		if m.Role == RoleTool {
			total += utf8.RuneCountInString(m.Content)
		}
	}
	var out []Message
	for i := 0; i < newest && total > budget; i++ {
		m := msgs[i]
		n := utf8.RuneCountInString(m.Content)
		if m.Role != RoleTool || n < 2*ToolHistoryKept {
			continue
		}
		if out == nil {
			out = append([]Message(nil), msgs...)
		}
		out[i].Content = ClipNoted(m.Content, ToolHistoryKept, "; cut from the history to save room, call the tool again if you need the rest")
		total -= n - utf8.RuneCountInString(out[i].Content)
	}
	if out == nil {
		return msgs
	}
	return out
}
