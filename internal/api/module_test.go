package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

type pingModule struct{}

func (pingModule) ID() string { return "ping" }
func (pingModule) Routes(_ *Server, r chi.Router) {
	r.Get("/ping", func(w http.ResponseWriter, _ *http.Request) {
		WriteJSON(w, http.StatusOK, map[string]any{"pong": true})
	})
}
func (pingModule) Meta(_ *Server, out map[string]any) { out["ping_flavours"] = []string{"plain"} }

// A module's routes sit under /api/v1 next to the core's, and its constants land in /meta.
func TestModulesMountRoutesAndMeta(t *testing.T) {
	s := &Server{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Modules: []Module{pingModule{}}}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/ping")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("module route: %v %v", err, resp)
	}
	resp.Body.Close()

	resp, err = http.Get(srv.URL + "/api/v1/meta")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var meta struct {
		Modules      []string `json:"modules"`
		PingFlavours []string `json:"ping_flavours"`
		Statuses     []string `json:"statuses"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		t.Fatal(err)
	}
	if len(meta.Modules) != 1 || meta.Modules[0] != "ping" || len(meta.PingFlavours) != 1 {
		t.Fatalf("meta: %+v", meta)
	}
	if meta.Statuses != nil {
		t.Fatalf("post statuses belong to the content module, which is not mounted: %+v", meta)
	}
}
