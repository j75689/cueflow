// Package publishermock is an in-memory publisher that follows the publisher MCP contract
// (docs/publisher-mcp.md): list_accounts, create_post, publish_post and create_upload over stateless
// Streamable HTTP at /mcp, behind an optional bearer token. cmd/mock-publisher-mcp serves it for local
// development and demos; the e2e tests run it in-process. Nothing reaches a real platform: posts are
// recorded and listed at GET /posts.
package publishermock

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Account is one account the mock can post to, as list_accounts reports it.
type Account struct {
	ID       string `json:"id"`
	Platform string `json:"platform"`
	Username string `json:"username"`
	Tag      string `json:"tag,omitempty"`
}

func (a Account) label() string { return a.Platform + " @" + a.Username }

// DefaultAccounts covers every platform of the contract plus a test-tagged account, which the
// connector refuses to mix with the others.
var DefaultAccounts = []Account{
	{ID: "th1", Platform: "threads", Username: "demo", Tag: "brand"},
	{ID: "ig1", Platform: "instagram", Username: "demo.ig", Tag: "brand"},
	{ID: "yt1", Platform: "youtube", Username: "demo-channel", Tag: "brand"},
	{ID: "th-test", Platform: "threads", Username: "sandbox", Tag: "test"},
}

// Part is one thread part of create_post.
type Part struct {
	Text         string   `json:"text"`
	ImageURLs    []string `json:"image_urls,omitempty"`
	ImagesBase64 []string `json:"images_base64,omitempty"`
	VideoURLs    []string `json:"video_urls,omitempty"`
	VideoKeys    []string `json:"video_keys,omitempty"`
}

// CreateRequest is what create_post received.
type CreateRequest struct {
	Parts       []Part   `json:"parts"`
	Accounts    []string `json:"accounts"`
	ScheduledAt string   `json:"scheduled_at,omitempty"`
	Topic       string   `json:"topic,omitempty"`
	Link        string   `json:"link,omitempty"`
}

// Result is the outcome for one account, as the contract's reply reports it.
type Result struct {
	Account   string `json:"account"`
	Label     string `json:"label"`
	Published bool   `json:"published"`
	Permalink string `json:"permalink,omitempty"`
	Error     string `json:"error,omitempty"`
}

// Post is a recorded post: the request and the reply CueFlow got.
type Post struct {
	ID         string        `json:"id"`
	Status     string        `json:"status"` // published or scheduled
	ReceivedAt time.Time     `json:"received_at"`
	Request    CreateRequest `json:"request"`
	Results    []Result      `json:"results"`
}

// Text joins the parts, for assertions and logs.
func (p Post) Text() string {
	parts := make([]string, 0, len(p.Request.Parts))
	for _, part := range p.Request.Parts {
		parts = append(parts, part.Text)
	}
	return strings.Join(parts, "\n\n")
}

type Server struct {
	// Token, when set, is required as "Authorization: Bearer <token>" on /mcp.
	Token    string
	Accounts []Account
	// Fail makes create_post refuse every post with a tool error, the way a publisher reports a
	// validation failure or a rate limit; nothing is recorded while it is set.
	Fail atomic.Bool
	// Logf, when set, is called for every received post.
	Logf func(format string, args ...any)

	mu      sync.Mutex
	posts   []*Post
	uploads map[string]int // upload key -> bytes received
	counter atomic.Int64
	base    atomic.Pointer[string] // scheme://host of the last MCP request, for upload URLs
}

func New(token string) *Server {
	return &Server{Token: token, Accounts: append([]Account(nil), DefaultAccounts...), uploads: map[string]int{}}
}

// Posts returns every recorded post, oldest first.
func (s *Server) Posts() []Post {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Post, 0, len(s.posts))
	for _, p := range s.posts {
		out = append(out, *p)
	}
	return out
}

// Uploads returns the bytes received per upload key.
func (s *Server) Uploads() map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]int, len(s.uploads))
	for k, v := range s.uploads {
		out[k] = v
	}
	return out
}

func (s *Server) account(id string) (Account, bool) {
	for _, a := range s.Accounts {
		if a.ID == id {
			return a, true
		}
	}
	return Account{}, false
}

func jsonResult(v any) *mcp.CallToolResult {
	b, _ := json.Marshal(v)
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}
}

func toolError(format string, args ...any) *mcp.CallToolResult {
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf(format, args...)}}}
}

func (s *Server) mcpServer() *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: "publisher-mock", Version: "0.1.0"}, nil)
	obj := map[string]any{"type": "object"}
	ro := true

	srv.AddTool(&mcp.Tool{Name: "list_accounts", Description: "The accounts a post can go to", InputSchema: obj,
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: ro}},
		func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			seen := map[string]bool{}
			var platforms []map[string]any
			for _, a := range s.Accounts {
				if !seen[a.Platform] {
					seen[a.Platform] = true
					platforms = append(platforms, map[string]any{"platform": a.Platform, "configured": true})
				}
			}
			return jsonResult(map[string]any{"accounts": s.Accounts, "platforms": platforms}), nil
		})

	srv.AddTool(&mcp.Tool{Name: "create_post", Description: "Publish or schedule a post to the chosen accounts", InputSchema: obj},
		func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if s.Fail.Load() {
				return toolError("simulated rate limit from the mock publisher"), nil
			}
			var in CreateRequest
			if err := json.Unmarshal(req.Params.Arguments, &in); err != nil {
				return toolError("bad arguments: %v", err), nil
			}
			if len(in.Accounts) == 0 {
				return toolError("accounts is required"), nil
			}
			var accounts []Account
			for _, id := range in.Accounts {
				a, ok := s.account(id)
				if !ok {
					return toolError("unknown account %q", id), nil
				}
				accounts = append(accounts, a)
			}
			post := &Post{ID: fmt.Sprintf("post-%d", s.counter.Add(1)), Status: "published", ReceivedAt: time.Now().UTC(), Request: in}
			if in.ScheduledAt != "" {
				post.Status = "scheduled"
			} else {
				for _, a := range accounts {
					post.Results = append(post.Results, Result{Account: a.ID, Label: a.label(), Published: true,
						Permalink: fmt.Sprintf("https://%s.example/%s/%s", a.Platform, a.Username, post.ID)})
				}
			}
			s.mu.Lock()
			s.posts = append(s.posts, post)
			s.mu.Unlock()
			if s.Logf != nil {
				s.Logf("received %s post %s for %s (%d parts): %.80q", post.Status, post.ID, strings.Join(in.Accounts, ","), len(in.Parts), post.Text())
			}
			return jsonResult(map[string]any{"id": post.ID, "status": post.Status, "results": post.Results}), nil
		})

	srv.AddTool(&mcp.Tool{Name: "publish_post", Description: "Retry a post on the accounts it has not reached", InputSchema: obj},
		func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			var in struct {
				ID string `json:"id"`
			}
			_ = json.Unmarshal(req.Params.Arguments, &in)
			s.mu.Lock()
			defer s.mu.Unlock()
			for _, p := range s.posts {
				if p.ID != in.ID {
					continue
				}
				p.Status = "published"
				if len(p.Results) == 0 {
					for _, id := range p.Request.Accounts {
						a, _ := s.account(id)
						p.Results = append(p.Results, Result{Account: a.ID, Label: a.label()})
					}
				}
				for i := range p.Results {
					p.Results[i].Published, p.Results[i].Error = true, ""
					if p.Results[i].Permalink == "" {
						a, _ := s.account(p.Results[i].Account)
						p.Results[i].Permalink = fmt.Sprintf("https://%s.example/%s/%s", a.Platform, a.Username, p.ID)
					}
				}
				return jsonResult(map[string]any{"id": p.ID, "status": p.Status, "results": p.Results}), nil
			}
			return toolError("no post %q", in.ID), nil
		})

	srv.AddTool(&mcp.Tool{Name: "create_upload", Description: "A signed URL to upload a video to", InputSchema: obj},
		func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			var in struct {
				ContentType string `json:"content_type"`
				Size        int    `json:"size"`
			}
			_ = json.Unmarshal(req.Params.Arguments, &in)
			base := ""
			if b := s.base.Load(); b != nil {
				base = *b
			}
			key := fmt.Sprintf("uploads/upload-%d", s.counter.Add(1))
			return jsonResult(map[string]any{"upload_url": base + "/" + key, "method": http.MethodPut, "key": key,
				"headers": map[string]string{"Content-Type": in.ContentType}}), nil
		})
	return srv
}

// Handler serves /mcp (the MCP endpoint), PUT /uploads/{name} (the signed upload URLs create_upload
// hands out), GET /posts (received posts as JSON) and GET /health.
func (s *Server) Handler() http.Handler {
	srv := s.mcpServer()
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	mux := http.NewServeMux()
	mux.Handle("/mcp", s.requireBearer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		base := scheme + "://" + r.Host
		s.base.Store(&base)
		mcpHandler.ServeHTTP(w, r)
	})))
	mux.HandleFunc("PUT /uploads/{name}", func(w http.ResponseWriter, r *http.Request) {
		n, err := io.Copy(io.Discard, r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		s.uploads["uploads/"+r.PathValue("name")] = int(n)
		s.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /posts", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(s.Posts())
	})
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) })
	return mux
}

func (s *Server) requireBearer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.Token != "" {
			tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if subtle.ConstantTimeCompare([]byte(tok), []byte(s.Token)) != 1 {
				http.Error(w, "invalid bearer token", http.StatusUnauthorized)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
