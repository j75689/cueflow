package content

import (
	"context"
	"net/http"
	"time"

	"github.com/j75689/cueflow/internal/api"
	"github.com/j75689/cueflow/internal/connector"
	"github.com/j75689/cueflow/internal/hub"
)

func (h *handlers) fetchConnector(w http.ResponseWriter, r *http.Request) {
	id, err := api.PathID(r)
	if err != nil {
		h.s.Fail(w, err)
		return
	}
	c, err := h.m.Store.GetConnector(r.Context(), id)
	if err != nil {
		h.s.Fail(w, err)
		return
	}
	if c.Kind != connector.KindSource {
		api.WriteError(w, http.StatusBadRequest, "invalid_argument", "only source connectors can fetch")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	n, err := FetchSource(ctx, h.m.Hub, h.m.Store, id, time.Now().Add(-7*24*time.Hour), 0)
	if err != nil {
		api.WriteError(w, http.StatusBadGateway, "upstream_error", err.Error())
		return
	}
	h.m.Broker.PublishGlobal("connector.updated", map[string]any{"id": id})
	api.WriteJSON(w, http.StatusOK, map[string]int{"new_items": n})
}

func (h *handlers) connectorItems(w http.ResponseWriter, r *http.Request) {
	id, err := api.PathID(r)
	if err != nil {
		h.s.Fail(w, err)
		return
	}
	items, err := h.m.Store.ListItems(r.Context(), id, api.QueryInt(r, "limit", 50))
	if err != nil {
		h.s.Fail(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, items)
}

// connectorAccounts lists the accounts a per-account publisher (e.g. an MCP publisher) can post to.
func (h *handlers) connectorAccounts(w http.ResponseWriter, r *http.Request) {
	id, err := api.PathID(r)
	if err != nil {
		h.s.Fail(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	inst, c, err := h.m.Hub.Instance(ctx, id, true)
	if err != nil {
		h.s.Fail(w, err)
		return
	}
	defer hub.Close(inst)
	spec, _ := connector.Lookup(c.Type)
	al, ok := inst.(connector.AccountLister)
	if !ok || !spec.Accounts {
		api.WriteError(w, http.StatusBadRequest, "invalid_argument", "this connector does not post to individual accounts")
		return
	}
	accounts, err := al.ListAccounts(ctx)
	if err != nil {
		api.WriteError(w, http.StatusBadGateway, "upstream_error", err.Error())
		return
	}
	api.WriteJSON(w, http.StatusOK, accounts)
}
