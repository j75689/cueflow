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

// TrimToolHistory is how a connector keeps Request.ToolHistoryBudget. It replays the run request by
// request (each assistant message was produced from the messages before it): whenever the tool
// results a request carries pass budget, the oldest are cut to ToolHistoryKept characters until they
// are under half of it. Cutting to half rather than to the budget means a cut lands every few turns
// instead of on every one. The newest turn's results are never cut. The result depends only on msgs,
// so every request sends earlier turns exactly as the previous one did, except where a new cut lands.
// msgs itself is not changed.
//
// stale[i] is true for an assistant message that was produced before a cut landed among the turns it
// had seen: what it was produced from has changed since. Connectors that bind state to the turns
// before it (Anthropic's thinking blocks) drop that state for stale messages. stale is nil when
// nothing was cut.
func TrimToolHistory(msgs []Message, budget int) (out []Message, stale []bool) {
	out = msgs
	if budget <= 0 {
		return out, nil
	}
	total, next := 0, 0     // next: the first message not yet considered for a cut
	shed := func(end int) { // the request that carried out[:end]
		if total <= budget {
			return
		}
		newest := end
		for newest > 0 && out[newest-1].Role == RoleTool {
			newest--
		}
		first := -1
		for ; next < newest && total > budget/2; next++ {
			n := utf8.RuneCountInString(out[next].Content)
			if out[next].Role != RoleTool || n < 2*ToolHistoryKept {
				continue
			}
			if stale == nil {
				out = append([]Message(nil), msgs...)
				stale = make([]bool, len(msgs))
			}
			if first < 0 {
				first = next
			}
			out[next].Content = ClipNoted(out[next].Content, ToolHistoryKept, "; cut from the history to save room, call the tool again if you need the rest")
			total -= n - utf8.RuneCountInString(out[next].Content)
		}
		for j := first + 1; first >= 0 && j < end; j++ {
			if out[j].Role == RoleAssistant {
				stale[j] = true
			}
		}
	}
	for i, m := range msgs {
		switch m.Role {
		case RoleAssistant:
			shed(i)
		case RoleTool:
			total += utf8.RuneCountInString(m.Content)
		}
	}
	shed(len(msgs))
	return out, stale
}
