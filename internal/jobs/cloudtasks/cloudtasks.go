// Package cloudtasks is a jobs.Queue that delivers work through Google Cloud Tasks. It speaks the
// REST API directly (no SDK): each Enqueue creates an HTTP task that Cloud Tasks POSTs to this
// service's /internal/work/{kind}/{id}, signed with an OIDC token.
package cloudtasks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/oauth2/google"

	"github.com/j75689/cueflow/internal/jobs"
)

// DefaultEndpoint is the Cloud Tasks REST API origin.
const DefaultEndpoint = "https://cloudtasks.googleapis.com"

// dedupeBucket is the window in seconds within which enqueues of the same job collapse.
const dedupeBucket = 5

const maxAttempts = 3

// Config configures a Queue.
type Config struct {
	// Queue is "projects/P/locations/L/queues/Q".
	Queue string
	// TargetURL is the service URL Cloud Tasks calls (no trailing slash).
	TargetURL string
	// ServiceAccount is the email that signs the OIDC token.
	ServiceAccount string
	// Audience of the OIDC token; defaults to TargetURL.
	Audience string

	// DispatchDeadline is how long Cloud Tasks waits for a delivery to finish (max 30m); default 30m.
	DispatchDeadline time.Duration

	// Client is the authenticated HTTP client; defaults to Google application default credentials.
	Client *http.Client
	// Endpoint overrides DefaultEndpoint (tests).
	Endpoint string
	// Sleep overrides time.Sleep-style waiting between retries (tests).
	Sleep func(time.Duration)
	// Now overrides time.Now (tests).
	Now func() time.Time
}

// Queue implements jobs.Queue.
type Queue struct {
	cfg Config
}

var _ jobs.Queue = (*Queue)(nil)

// New validates cfg and builds a Queue.
func New(cfg Config) (*Queue, error) {
	cfg.Queue = strings.Trim(cfg.Queue, "/")
	cfg.TargetURL = strings.TrimRight(cfg.TargetURL, "/")
	parts := strings.Split(cfg.Queue, "/")
	if len(parts) != 6 || parts[0] != "projects" || parts[2] != "locations" || parts[4] != "queues" {
		return nil, fmt.Errorf("cloudtasks: queue %q must look like projects/P/locations/L/queues/Q", cfg.Queue)
	}
	if cfg.TargetURL == "" {
		return nil, errors.New("cloudtasks: target URL is required")
	}
	if cfg.ServiceAccount == "" {
		return nil, errors.New("cloudtasks: service account is required")
	}
	if cfg.Audience == "" {
		cfg.Audience = cfg.TargetURL
	}
	if cfg.DispatchDeadline <= 0 {
		cfg.DispatchDeadline = 30 * time.Minute
	}
	if cfg.Endpoint == "" {
		cfg.Endpoint = DefaultEndpoint
	}
	cfg.Endpoint = strings.TrimRight(cfg.Endpoint, "/")
	if cfg.Client == nil {
		c, err := google.DefaultClient(context.Background(), "https://www.googleapis.com/auth/cloud-platform")
		if err != nil {
			return nil, fmt.Errorf("cloudtasks: default credentials: %w", err)
		}
		cfg.Client = c
	}
	if cfg.Sleep == nil {
		cfg.Sleep = time.Sleep
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Queue{cfg: cfg}, nil
}

type oidcToken struct {
	ServiceAccountEmail string `json:"serviceAccountEmail"`
	Audience            string `json:"audience"`
}

type httpRequest struct {
	URL        string    `json:"url"`
	HTTPMethod string    `json:"httpMethod"`
	OIDCToken  oidcToken `json:"oidcToken"`
}

type task struct {
	Name         string      `json:"name"`
	HTTPRequest  httpRequest `json:"httpRequest"`
	ScheduleTime string      `json:"scheduleTime,omitempty"`
	// DispatchDeadline is a Duration string such as "1800s".
	DispatchDeadline string `json:"dispatchDeadline"`
}

type createRequest struct {
	Task task `json:"task"`
}

// Enqueue creates the task. Repeating the same (kind, id) within the same 5 second window is a
// no-op because Cloud Tasks rejects the duplicate name with ALREADY_EXISTS.
func (q *Queue) Enqueue(ctx context.Context, kind jobs.Kind, id uuid.UUID, delay time.Duration) error {
	when := q.cfg.Now()
	t := task{
		DispatchDeadline: fmt.Sprintf("%ds", int(q.cfg.DispatchDeadline.Seconds())),
		HTTPRequest: httpRequest{
			URL:        q.cfg.TargetURL + jobs.WorkPath(kind, id),
			HTTPMethod: "POST",
			OIDCToken:  oidcToken{ServiceAccountEmail: q.cfg.ServiceAccount, Audience: q.cfg.Audience},
		},
	}
	if delay > 0 {
		when = when.Add(delay)
		t.ScheduleTime = when.UTC().Format(time.RFC3339)
	}
	t.Name = fmt.Sprintf("%s/tasks/%s-%s-%d", q.cfg.Queue, kind, id, when.Unix()/dedupeBucket)
	body, err := json.Marshal(createRequest{Task: t})
	if err != nil {
		return err
	}
	url := q.cfg.Endpoint + "/v2/" + q.cfg.Queue + "/tasks"

	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 {
			q.cfg.Sleep(time.Duration(attempt-1) * 500 * time.Millisecond)
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := q.cfg.Client.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("cloudtasks: create task: %w", err)
			if ctx.Err() != nil {
				return lastErr
			}
			continue
		}
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		switch {
		case resp.StatusCode >= 200 && resp.StatusCode < 300:
			return nil
		case resp.StatusCode == http.StatusConflict:
			return nil // ALREADY_EXISTS: an equivalent task is already pending
		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
			lastErr = fmt.Errorf("cloudtasks: create task: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(payload)))
		default:
			return fmt.Errorf("cloudtasks: create task: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(payload)))
		}
	}
	return lastErr
}
