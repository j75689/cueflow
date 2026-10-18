package crew

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/j75689/cueflow/internal/store"
)

func TestRegistryExposesModuleCapabilities(t *testing.T) {
	RegisterCapability(Capability{ID: "testing", Name: "Testing", Default: true,
		Prompt: func(*store.Agent) string { return "\n- Test everything." }})
	RegisterTool(ToolSpec{Name: "run_tests", Capability: "testing", Schema: Obj(nil),
		Run: func(_ context.Context, r *Run, _ json.RawMessage) (string, error) {
			r.Inc("runs")
			return JSONResult(map[string]int{"runs": r.Count("runs")})
		}})

	var cap *Capability
	for _, c := range Capabilities() {
		if c.ID == "testing" {
			cap = &c
		}
	}
	if cap == nil || !slices.Equal(cap.Tools, []string{"run_tests"}) {
		t.Fatalf("registered capability not in the catalog: %+v", cap)
	}
	for _, id := range []string{"research", "testing"} {
		if !store.ValidCapability(id) {
			t.Errorf("%s should be a valid capability", id)
		}
	}
	if store.ValidCapability("flying") {
		t.Error("unknown capability accepted")
	}
	if d := DefaultCapabilities(); !slices.Contains(d, "testing") || !slices.Contains(d, "research") {
		t.Fatalf("default capabilities: %v", d)
	}

	a := &store.Agent{Name: "T", Capabilities: []string{"testing"}, Language: "en"}
	prompt := buildSystemPrompt(a, &store.Task{Number: 1}, nil, nil)
	if !strings.Contains(prompt, "Test everything.") {
		t.Fatalf("prompt should carry only the agent's capabilities:\n%s", prompt)
	}

	var runTests tool
	for _, tl := range toolsFor(a) {
		if tl.name == "run_tests" {
			runTests = tl
		}
	}
	if runTests.run == nil {
		t.Fatal("tool not offered to an agent with its capability")
	}
	env := &runEnv{c: &Crew{}, task: &store.Task{}, agent: a}
	for want := 1; want <= 2; want++ {
		out, err := runTests.run(context.Background(), env, nil)
		if err != nil || !strings.Contains(out, `"runs":`+string(rune('0'+want))) {
			t.Fatalf("run %d: %q %v", want, out, err)
		}
	}
	if len(toolsFor(&store.Agent{Capabilities: []string{"research"}})) == 0 {
		t.Fatal("research tools missing")
	}
	for _, tl := range toolsFor(&store.Agent{Capabilities: []string{"research"}}) {
		if tl.name == "run_tests" {
			t.Fatalf("tool %s offered without its capability", tl.name)
		}
	}
}
