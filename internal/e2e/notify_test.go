package e2e

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/j75689/cueflow/internal/connector"
	"github.com/j75689/cueflow/internal/llm"
	"github.com/j75689/cueflow/internal/store"
)

// fakeNotifier records the alerts it was asked to send.
type fakeNotifier struct{}

var (
	alertsMu sync.Mutex
	alerts   []connector.Notification
)

func (fakeNotifier) Test(context.Context) (*connector.TestResult, error) {
	return &connector.TestResult{OK: true}, nil
}
func (fakeNotifier) Notify(_ context.Context, n connector.Notification) error {
	alertsMu.Lock()
	alerts = append(alerts, n)
	alertsMu.Unlock()
	return nil
}

func takeAlerts() []connector.Notification {
	alertsMu.Lock()
	defer alertsMu.Unlock()
	out := alerts
	alerts = nil
	return out
}

func init() {
	connector.Register(connector.Spec{Type: "fake_notifier", Kind: connector.KindNotifier, Name: "Fake notifier"},
		func(connector.Config, connector.Env) (connector.Instance, error) { return fakeNotifier{}, nil })
}

func TestTaskAlertsGoToTheOrgsNotifier(t *testing.T) {
	e, team := crewSetup(t)
	ctx := context.Background()
	e.crew.PublicURL = "https://cueflow.example/"
	org, _ := e.store.DefaultOrg(ctx)
	takeAlerts()

	setScript(t, func(t *testing.T, req llm.Request) llm.Message {
		b := brief(req)
		switch {
		case whoAmI(req.System) == "researcher":
			return calls("complete_task", map[string]any{"summary": "Found it."})
		case strings.Contains(b, "Ask me"):
			return calls("ask_human", map[string]any{"question": "Which region?"})
		case !strings.Contains(b, "## Subtasks"):
			return calls("create_subtask", map[string]any{"title": "Look it up", "description": "-", "assignee": "researcher"},
				"wait_for_subtasks", map[string]any{})
		}
		return calls("complete_task", map[string]any{"summary": "All done."})
	})
	run := func(title string) store.Task {
		t.Helper()
		var task store.Task
		if code := e.do("POST", "/tasks", map[string]any{"title": title, "assignee_agent_id": team["chief"].ID}, &task); code != 201 {
			t.Fatalf("create task: %d", code)
		}
		e.crew.RunPending(ctx, 10)
		return task
	}

	// No notifier picked: nothing is sent.
	run("Quiet one")
	if got := takeAlerts(); len(got) != 0 {
		t.Fatalf("alerts without a notifier: %+v", got)
	}

	// Only a notifier can be picked.
	var llmConn store.Connector
	for _, c := range mustConnectors(t, e) {
		if c.Kind == connector.KindLLM {
			llmConn = c
		}
	}
	if code := e.do("PATCH", "/orgs/"+org.ID.String(), map[string]any{"notifier_id": llmConn.ID}, nil); code != 400 {
		t.Fatalf("an LLM as notifier: %d", code)
	}
	var tg store.Connector
	if code := e.do("POST", "/connectors", map[string]any{"name": "TG", "type": "fake_notifier", "config": map[string]any{}}, &tg); code != 201 {
		t.Fatalf("create notifier: %d", code)
	}
	var got store.Org
	if code := e.do("PATCH", "/orgs/"+org.ID.String(), map[string]any{"notifier_id": tg.ID}, &got); code != 200 || got.NotifierID == nil || *got.NotifierID != tg.ID {
		t.Fatalf("pick notifier: %d %+v", code, got)
	}

	// The parent lands in REVIEW: one alert. Its agent-created subtask ends DONE and sends none.
	done := run("Find the numbers")
	sent := takeAlerts()
	if len(sent) != 1 {
		t.Fatalf("alerts after a delegated task: %+v", sent)
	}
	n := sent[0]
	if n.Subject != "✅ Ready for review: Find the numbers" || !strings.Contains(n.Text, "All done.") ||
		!strings.Contains(n.Text, "https://cueflow.example/tasks/view/?id="+done.ID.String()+"&org="+org.Slug) {
		t.Fatalf("review alert: %+v", n)
	}

	// A question.
	run("Ask me something")
	if sent := takeAlerts(); len(sent) != 1 || !strings.HasPrefix(sent[0].Subject, "❓") || !strings.Contains(sent[0].Text, "Which region?") {
		t.Fatalf("question alert: %+v", sent)
	}

	// A run cut off by a restart.
	stuck := run("Ask me again")
	takeAlerts()
	if _, err := e.pool.Exec(ctx, `UPDATE tasks SET status='IN_PROGRESS', lease_until = NOW() - INTERVAL '1 minute' WHERE id=$1`, stuck.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.crew.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if sent := takeAlerts(); len(sent) != 1 || !strings.HasPrefix(sent[0].Subject, "⚠️ Interrupted") || !strings.Contains(sent[0].Text, "Chief") {
		t.Fatalf("interrupted alert: %+v", sent)
	}

	// A disabled notifier mutes alerts; "" turns them off.
	if code := e.do("PATCH", "/connectors/"+tg.ID.String(), map[string]any{"is_active": false}, nil); code != 200 {
		t.Fatalf("disable notifier: %d", code)
	}
	run("Ask me once more")
	if sent := takeAlerts(); len(sent) != 0 {
		t.Fatalf("alerts from a disabled notifier: %+v", sent)
	}
	if code := e.do("PATCH", "/orgs/"+org.ID.String(), map[string]any{"notifier_id": ""}, &got); code != 200 || got.NotifierID != nil {
		t.Fatalf("clear notifier: %d %+v", code, got)
	}
}

func mustConnectors(t *testing.T, e *env) []store.Connector {
	t.Helper()
	var list []store.Connector
	if code := e.do("GET", "/connectors", nil, &list); code != 200 {
		t.Fatalf("list connectors: %d", code)
	}
	return list
}
