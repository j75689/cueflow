package api

import (
	"net/http"

	"github.com/j75689/cueflow/internal/crew"
	"github.com/j75689/cueflow/internal/store"
)

// OrgParam names the org a request works in (id or slug); blank means the default org.
const OrgParam = "org"

// reqOrg resolves the org of an org-scoped list or create request.
func (s *Server) ReqOrg(r *http.Request) (*store.Org, error) {
	o, err := s.Store.ResolveOrg(r.Context(), r.URL.Query().Get(OrgParam))
	if err != nil {
		return nil, &store.ValidationError{Msg: "unknown org " + r.URL.Query().Get(OrgParam)}
	}
	return o, nil
}

func (s *Server) listOrgs(w http.ResponseWriter, r *http.Request) {
	orgs, err := s.Store.ListOrgs(r.Context())
	if err != nil {
		s.Fail(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, orgs)
}

type orgBody struct {
	Name *string `json:"name"`
	Slug *string `json:"slug"`
	// SeedAgents creates the starter Chief agent in a new org.
	SeedAgents bool `json:"seed_agents"`
	// Team is a team template (see /meta agent_teams) to apply to the new org.
	Team string `json:"team"`
}

func (s *Server) createOrg(w http.ResponseWriter, r *http.Request) {
	var body orgBody
	if err := Decode(r, &body); err != nil {
		s.Fail(w, err)
		return
	}
	if body.Team != "" {
		if _, ok := crew.TeamByID(body.Team); !ok {
			s.Fail(w, store.Invalid("unknown team "+body.Team))
			return
		}
	}
	o := &store.Org{}
	if body.Name != nil {
		o.Name = *body.Name
	}
	if body.Slug != nil {
		o.Slug = *body.Slug
	}
	out, err := s.Store.CreateOrg(r.Context(), o)
	if err != nil {
		s.Fail(w, err)
		return
	}
	if body.SeedAgents {
		if _, err := crew.SeedDefaults(r.Context(), s.Store, out.ID); err != nil {
			s.Logger.Warn("seed org agents", "org", out.Slug, "err", err)
		}
	}
	if body.Team != "" {
		if _, _, err := crew.ApplyTeam(r.Context(), s.Store, out.ID, body.Team); err != nil {
			s.Logger.Warn("apply team template", "org", out.Slug, "team", body.Team, "err", err)
		}
	}
	s.Broker.Publish("org.updated", map[string]any{"id": out.ID})
	WriteJSON(w, http.StatusCreated, out)
}

func (s *Server) updateOrg(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r)
	if err != nil {
		s.Fail(w, err)
		return
	}
	cur, err := s.Store.GetOrg(r.Context(), id)
	if err != nil {
		s.Fail(w, err)
		return
	}
	var body orgBody
	if err := Decode(r, &body); err != nil {
		s.Fail(w, err)
		return
	}
	if body.Name != nil {
		cur.Name = *body.Name
	}
	if body.Slug != nil {
		cur.Slug = *body.Slug
	}
	out, err := s.Store.UpdateOrg(r.Context(), cur)
	if err != nil {
		s.Fail(w, err)
		return
	}
	s.Broker.Publish("org.updated", map[string]any{"id": out.ID})
	WriteJSON(w, http.StatusOK, out)
}

func (s *Server) deleteOrg(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r)
	if err != nil {
		s.Fail(w, err)
		return
	}
	if err := s.Store.DeleteOrg(r.Context(), id); err != nil {
		s.Fail(w, err)
		return
	}
	s.Broker.Publish("org.updated", map[string]any{"id": id})
	w.WriteHeader(http.StatusNoContent)
}
