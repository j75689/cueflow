package rss

import (
	"testing"

	"github.com/mmcdole/gofeed"
)

func TestEntriesSharingALinkKeepTheirOwnIDs(t *testing.T) {
	feed := "https://trends.google.com/trending/rss?geo=JP"
	items := []*gofeed.Item{
		{Title: "谷川萌々子", Link: feed},
		{Title: "hikakin", Link: feed},
		{Title: "own page", Link: "https://example.com/a"},
		{Title: "with guid", Link: feed, GUID: "g1"},
	}
	shared := sharedLinks(items)
	ids := map[string]bool{}
	for _, e := range items {
		ids[externalID(e, shared[e.Link])] = true
	}
	if len(ids) != len(items) {
		t.Fatalf("got %d distinct IDs for %d entries: %v", len(ids), len(items), ids)
	}
	if got := externalID(items[2], shared[items[2].Link]); got != "https://example.com/a" {
		t.Fatalf("an entry with its own link should keep the link as ID, got %q", got)
	}
	if got := externalID(items[3], shared[items[3].Link]); got != "g1" {
		t.Fatalf("a GUID should win, got %q", got)
	}
}
