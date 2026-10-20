package llm

import (
	"unicode/utf8"

	"github.com/j75689/cueflow/internal/textutil"
)

const (
	// ToolHistoryKept is what TrimToolHistory leaves of a big result it cuts.
	ToolHistoryKept = 2000
	// toolHistoryStub replaces a cut result too small to be worth keeping a head of; results shorter
	// than toolHistoryTiny are left alone.
	toolHistoryStub = "(removed from the history to save room; call the tool again if you need it)"
	toolHistoryTiny = 200
)

// TrimToolHistory is how a connector keeps Request.ToolHistoryBudget. It replays the run request by
// request (each assistant message was produced from the messages before it): whenever the tool
// results a request carries pass budget, the oldest are cut until the results that may be cut (all
// but the newest turn's, which are never cut) are under half of it. A result of at least twice
// ToolHistoryKept keeps its first ToolHistoryKept characters; a smaller one is replaced by a short
// stub. Cutting to half rather than to the budget means that, unless single results are near half
// the budget themselves, a cut lands every few turns instead of on every one. The result depends only
// on msgs, so every request sends earlier turns exactly as the previous one did, except where a new
// cut lands. msgs itself is not changed.
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
		newest, fixed := end, 0 // fixed: the newest turn's results, which are never cut
		for newest > 0 && out[newest-1].Role == RoleTool {
			newest--
			fixed += utf8.RuneCountInString(out[newest].Content)
		}
		first := -1
		for ; next < newest && total-fixed > budget/2; next++ {
			n := utf8.RuneCountInString(out[next].Content)
			if out[next].Role != RoleTool || n < toolHistoryTiny {
				continue
			}
			if stale == nil {
				out = append([]Message(nil), msgs...)
				stale = make([]bool, len(msgs))
			}
			if first < 0 {
				first = next
			}
			if n >= 2*ToolHistoryKept {
				out[next].Content = textutil.ClipNoted(out[next].Content, ToolHistoryKept, "; cut from the history to save room, call the tool again if you need the rest")
			} else {
				out[next].Content = toolHistoryStub
			}
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
