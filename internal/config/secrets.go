package config

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2/google"
)

// SecretVars are the variables that hold a credential. Each one can also be given as a Secret Manager
// reference (gcpsm://...) or through <NAME>_FILE; see resolveSecret.
var SecretVars = []string{
	"DATABASE_URL", "DATABASE_SESSION_URL",
	"CUEFLOW_SECRET_KEY", "CUEFLOW_SECRET_KEY_PREVIOUS",
	"CUEFLOW_ADMIN_TOKEN", "CUEFLOW_MCP_TOKEN",
}

// SecretManager reads one secret version. name is "projects/P/secrets/S/versions/V".
type SecretManager interface {
	Access(ctx context.Context, name string) ([]byte, error)
}

// newSecretManager builds the Secret Manager client the first time a gcpsm:// reference is met, so a
// deployment that uses none needs no Google credentials. Tests replace it.
var newSecretManager = func() (SecretManager, error) {
	c, err := google.DefaultClient(context.Background(), "https://www.googleapis.com/auth/cloud-platform")
	if err != nil {
		return nil, fmt.Errorf("default credentials: %w", err)
	}
	cc := *c
	cc.Timeout = SecretFetchTimeout // also bounds the token fetch, which does not see the request's ctx
	return &gcpSecretManager{client: &cc, endpoint: "https://secretmanager.googleapis.com/v1/"}, nil
}

// SecretFetchTimeout bounds reading one Secret Manager reference at startup.
const SecretFetchTimeout = 10 * time.Second

const secretRefPrefix = "gcpsm://"

// secretRef is a Secret Manager reference: gcpsm://projects/<project>/secrets/<secret>[/versions/<version>].
var secretRef = regexp.MustCompile(`^gcpsm://projects/([A-Za-z0-9._:-]+)/secrets/([A-Za-z0-9_-]+)(?:/versions/([A-Za-z0-9_-]+))?$`)

// secrets resolves SecretVars for one Load, making one Secret Manager client at most.
type secrets struct {
	once sync.Once
	sm   SecretManager
	err  error
}

// resolve returns the value of the secret variable name, in this order:
//
//  1. name set and non-empty: a value starting with gcpsm:// is read from Secret Manager, anything
//     else is used as is;
//  2. name_FILE set: that file, less one trailing newline;
//  3. neither: "".
//
// A reference that can't be read is an error naming the variable and the reference, never the value.
func (s *secrets) resolve(name string) (string, error) {
	if v := os.Getenv(name); v != "" {
		if !strings.HasPrefix(v, secretRefPrefix) {
			return v, nil
		}
		m := secretRef.FindStringSubmatch(v)
		if m == nil {
			// The value itself is not printed: a malformed reference might be a mistyped secret.
			return "", fmt.Errorf("%s: malformed Secret Manager reference: want gcpsm://projects/<project>/secrets/<secret>[/versions/<version>]", name)
		}
		version := m[3]
		if version == "" {
			version = "latest"
		}
		ref := "projects/" + m[1] + "/secrets/" + m[2] + "/versions/" + version
		s.once.Do(func() { s.sm, s.err = newSecretManager() })
		if s.err != nil {
			return "", fmt.Errorf("%s: reading Secret Manager secret %s: %w", name, ref, s.err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), SecretFetchTimeout)
		defer cancel()
		b, err := s.sm.Access(ctx, ref)
		if err != nil {
			return "", fmt.Errorf("%s: reading Secret Manager secret %s: %w", name, ref, err)
		}
		return string(b), nil
	}
	if path := os.Getenv(name + "_FILE"); path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("%s_FILE: %w", name, err) // the error names the path, not the content
		}
		v := strings.TrimSuffix(string(b), "\n") // echo adds one
		return strings.TrimSuffix(v, "\r"), nil
	}
	return "", nil
}

// gcpSecretManager is Secret Manager's REST API: GET v1/{name}:access.
type gcpSecretManager struct {
	client   *http.Client
	endpoint string
}

func (g *gcpSecretManager) Access(ctx context.Context, name string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.endpoint+name+":access", nil)
	if err != nil {
		return nil, err
	}
	resp, err := g.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		// An error body carries a message such as "Permission ... denied", never a payload.
		var e struct {
			Error struct {
				Message string `json:"message"`
				Status  string `json:"status"`
			} `json:"error"`
		}
		if json.Unmarshal(body, &e) == nil && e.Error.Message != "" {
			return nil, fmt.Errorf("%s (HTTP %d %s)", e.Error.Message, resp.StatusCode, e.Error.Status)
		}
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var out struct {
		Payload struct {
			Data       string `json:"data"`
			DataCrc32C string `json:"dataCrc32c"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, errors.New("unreadable response")
	}
	data, err := base64.StdEncoding.DecodeString(out.Payload.Data)
	if err != nil {
		return nil, errors.New("unreadable payload")
	}
	if out.Payload.DataCrc32C != "" {
		want, err := strconv.ParseUint(out.Payload.DataCrc32C, 10, 32)
		if err != nil || crc32.Checksum(data, crc32.MakeTable(crc32.Castagnoli)) != uint32(want) {
			return nil, errors.New("payload checksum mismatch")
		}
	}
	return data, nil
}
