package connector

import "testing"

func TestDefaultPermission(t *testing.T) {
	cases := []struct {
		tool ExternalTool
		want string
	}{
		{ExternalTool{Name: "search_pages"}, PermAllow},
		{ExternalTool{Name: "getUser"}, PermAllow},
		{ExternalTool{Name: "summarize", ReadOnly: true}, PermAllow},
		{ExternalTool{Name: "create_page"}, PermAsk},
		{ExternalTool{Name: "list_and_delete", ReadOnly: true}, PermAsk}, // risky name beats the hint
		{ExternalTool{Name: "read_file", Destructive: true}, PermAsk},
		{ExternalTool{Name: "send_email"}, PermAsk},
		{ExternalTool{Name: "frobnicate"}, PermAsk},
	}
	for _, c := range cases {
		if got := DefaultPermission(c.tool); got != c.want {
			t.Errorf("%s: %s, want %s", c.tool.Name, got, c.want)
		}
	}
	if !RiskyTool(ExternalTool{Name: "publish_post"}) || RiskyTool(ExternalTool{Name: "search"}) {
		t.Error("risky classification")
	}
}
