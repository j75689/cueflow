package crew

import (
	"testing"

	"github.com/j75689/cueflow/internal/connector"
	"github.com/j75689/cueflow/internal/connectors/runnerpool"
)

// The thread says which runners may take the work: every label, one of them, or any runner.
func TestRunnerLabelsText(t *testing.T) {
	for _, c := range []struct {
		cfg  connector.Config
		want string
	}{
		{connector.Config{"labels": []any{"mac", "gpu"}}, "labels: mac, gpu"},
		{connector.Config{"labels": []any{"mac", "gpu"}, "label_match": "all"}, "labels: mac, gpu"},
		{connector.Config{"labels": []any{"mac", "gpu"}, "label_match": "any"}, "any of labels: mac, gpu"},
		{connector.Config{"label_match": "any"}, "any runner"},
	} {
		inst, err := runnerpool.New(c.cfg, connector.Env{})
		if err != nil {
			t.Fatal(err)
		}
		if got := runnerLabels(inst.(runnerPool)); got != c.want {
			t.Errorf("%v: %q, want %q", c.cfg, got, c.want)
		}
	}
}
