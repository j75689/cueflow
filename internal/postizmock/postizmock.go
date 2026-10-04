// Package postizmock is an in-memory stand-in for the Postiz MCP server, used by
// cmd/mock-postiz-mcp for local development and by tests. It exposes the tool names and result
// shapes of the real server (integrationList, uploadFromUrlTool, integrationSchedulePostTool)
// over stateless Streamable HTTP at /api/mcp with Bearer auth.
package postizmock

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type Channel struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Platform string `json:"platform"`
	Disabled bool   `json:"disabled"`
}

var DefaultChannels = []Channel{
	{ID: "int-threads-1", Name: "Demo Threads", Platform: "threads"},
	{ID: "int-instagram-1", Name: "Demo Instagram", Platform: "instagram-standalone"},
	{ID: "int-x-1", Name: "Demo X", Platform: "x"},
	{ID: "int-linkedin-1", Name: "Demo LinkedIn", Platform: "linkedin"},
}

type PostItem struct {
	Content     string   `json:"content"`
	Attachments []string `json:"attachments"`
}

type SocialPost struct {
	IntegrationID    string           `json:"integrationId"`
	IsPremium        bool             `json:"isPremium"`
	Date             string           `json:"date"`
	ShortLink        bool             `json:"shortLink"`
	Type             string           `json:"type"`
	PostsAndComments []PostItem       `json:"postsAndComments"`
	Settings         []map[string]any `json:"settings"`
}

type Received struct {
	PostID     string     `json:"postId"`
	ReceivedAt time.Time  `json:"received_at"`
	Post       SocialPost `json:"post"`
}

type Server struct {
	Key      string
	Channels []Channel
	// Fail makes integrationSchedulePostTool return a Postiz-style validation error.
	Fail atomic.Bool
	// Logf, when set, is called for every received post.
	Logf func(format string, args ...any)

	mu      sync.Mutex
	posts   []Received
	counter atomic.Int64
}

func New(key string) *Server {
	return &Server{Key: key, Channels: DefaultChannels}
}

func (s *Server) Posts() []Received {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Received(nil), s.posts...)
}

func (s *Server) known(id string) bool {
	for _, c := range s.Channels {
		if c.ID == id {
			return true
		}
	}
	return false
}

func (s *Server) mcpServer() *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: "postiz-mock", Version: "0.1.0"}, nil)

	mcp.AddTool(srv, &mcp.Tool{Name: "integrationList", Description: "List connected social channels"},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ map[string]any) (*mcp.CallToolResult, map[string]any, error) {
			return nil, map[string]any{"output": s.Channels}, nil
		})

	mcp.AddTool(srv, &mcp.Tool{Name: "uploadFromUrlTool", Description: "Upload a media file from a URL"},
		func(ctx context.Context, _ *mcp.CallToolRequest, in struct {
			URL string `json:"url"`
		}) (*mcp.CallToolResult, map[string]any, error) {
			if !strings.HasPrefix(in.URL, "http") {
				return nil, map[string]any{"error": "invalid url"}, nil
			}
			n := s.counter.Add(1)
			return nil, map[string]any{"id": fmt.Sprintf("media-%d", n), "path": fmt.Sprintf("/uploads/media-%d", n)}, nil
		})

	mcp.AddTool(srv, &mcp.Tool{Name: "integrationSchedulePostTool", Description: "Schedule or publish posts"},
		func(ctx context.Context, _ *mcp.CallToolRequest, in struct {
			SocialPost []SocialPost `json:"socialPost"`
		}) (*mcp.CallToolResult, map[string]any, error) {
			if s.Fail.Load() {
				return nil, map[string]any{"output": map[string]any{"errors": "simulated rate limit from mock Postiz"}}, nil
			}
			s.mu.Lock()
			defer s.mu.Unlock()
			var out []map[string]any
			for _, p := range in.SocialPost {
				if !s.known(p.IntegrationID) {
					return nil, nil, fmt.Errorf("Integration not found")
				}
				id := fmt.Sprintf("post-%d", s.counter.Add(1))
				s.posts = append(s.posts, Received{PostID: id, ReceivedAt: time.Now().UTC(), Post: p})
				if s.Logf != nil {
					first := ""
					if len(p.PostsAndComments) > 0 {
						first = p.PostsAndComments[0].Content
					}
					s.Logf("received %s post for %s (%d parts): %.80q", p.Type, p.IntegrationID, len(p.PostsAndComments), first)
				}
				out = append(out, map[string]any{"postId": id, "integration": p.IntegrationID, "previewUrl": "/p/" + id + "?share=true"})
			}
			return nil, map[string]any{"output": out}, nil
		})

	mcp.AddTool(srv, &mcp.Tool{Name: "postsListTool", Description: "List posts", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ map[string]any) (*mcp.CallToolResult, map[string]any, error) {
			return nil, map[string]any{"output": s.Posts()}, nil
		})
	return srv
}

// Handler serves /api/mcp (MCP), GET /posts (received posts as JSON) and GET /health.
func (s *Server) Handler() http.Handler {
	srv := s.mcpServer()
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	mux := http.NewServeMux()
	mux.Handle("/api/mcp", s.requireBearer(mcpHandler))
	mux.HandleFunc("GET /posts", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(s.Posts())
	})
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) })
	return mux
}

func (s *Server) requireBearer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(tok), []byte(s.Key)) != 1 {
			http.Error(w, "Invalid API Key or OAuth token", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
