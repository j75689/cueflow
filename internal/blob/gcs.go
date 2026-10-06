package blob

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"cloud.google.com/go/compute/metadata"
	"cloud.google.com/go/storage"
	"golang.org/x/oauth2/google"
	iam "google.golang.org/api/iamcredentials/v1"
)

// SignBytesFunc signs a payload with a service account's key (IAM SignBlob, no key file needed).
type SignBytesFunc func(ctx context.Context, accessID string, payload []byte) ([]byte, error)

// GCS keeps one object per id under bucket/prefix.
type GCS struct {
	client *storage.Client
	bucket string
	prefix string

	mu        sync.Mutex
	accessID  string        // service account email used to sign URLs; resolved lazily
	signBytes SignBytesFunc // defaults to IAM Credentials SignBlob
}

var (
	_ Store  = (*GCS)(nil)
	_ Signer = (*GCS)(nil)
)

// NewGCS connects with application default credentials (or STORAGE_EMULATOR_HOST). prefix may be
// empty.
func NewGCS(ctx context.Context, bucket, prefix string) (*GCS, error) {
	if bucket == "" {
		return nil, errors.New("gcs: bucket is required")
	}
	c, err := storage.NewClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("gcs: %w", err)
	}
	return NewGCSWithClient(c, bucket, prefix), nil
}

// NewGCSWithClient wraps an existing client (tests, custom auth).
func NewGCSWithClient(c *storage.Client, bucket, prefix string) *GCS {
	return &GCS{client: c, bucket: bucket, prefix: strings.Trim(prefix, "/")}
}

// SetSigning overrides how SignedURL signs: the service account email and a signer. Used in tests
// and when the account cannot be discovered from the environment.
func (g *GCS) SetSigning(accessID string, sign SignBytesFunc) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.accessID, g.signBytes = accessID, sign
}

func (g *GCS) Kind() string { return KindGCS }

func (g *GCS) object(id string) (*storage.ObjectHandle, error) {
	if id == "" || strings.ContainsAny(id, `/\`) || id == "." || id == ".." {
		return nil, fmt.Errorf("blob: bad id %q", id)
	}
	name := id
	if g.prefix != "" {
		name = g.prefix + "/" + id
	}
	return g.client.Bucket(g.bucket).Object(name), nil
}

func (g *GCS) Put(ctx context.Context, id string, data []byte) error {
	o, err := g.object(id)
	if err != nil {
		return err
	}
	w := o.NewWriter(ctx)
	if _, err := w.Write(data); err != nil {
		_ = w.Close()
		return err
	}
	return w.Close()
}

func (g *GCS) Open(ctx context.Context, id string) (io.ReadSeekCloser, int64, error) {
	o, err := g.object(id)
	if err != nil {
		return nil, 0, err
	}
	attrs, err := o.Attrs(ctx)
	if errors.Is(err, storage.ErrObjectNotExist) {
		return nil, 0, ErrNotFound
	}
	if err != nil {
		return nil, 0, err
	}
	return &gcsReader{ctx: ctx, obj: o, size: attrs.Size}, attrs.Size, nil
}

func (g *GCS) Delete(ctx context.Context, id string) error {
	o, err := g.object(id)
	if err != nil {
		return err
	}
	if err := o.Delete(ctx); err != nil && !errors.Is(err, storage.ErrObjectNotExist) {
		return err
	}
	return nil
}

// SignedURL returns a V4 signed GET URL valid for ttl. When mimeType is set the download is served
// with that Content-Type.
func (g *GCS) SignedURL(ctx context.Context, id string, ttl time.Duration, mimeType string) (string, error) {
	o, err := g.object(id)
	if err != nil {
		return "", err
	}
	accessID, sign, err := g.signer(ctx)
	if err != nil {
		return "", err
	}
	opts := &storage.SignedURLOptions{
		Scheme:         storage.SigningSchemeV4,
		Method:         "GET",
		Expires:        time.Now().Add(ttl),
		GoogleAccessID: accessID,
		SignBytes:      func(b []byte) ([]byte, error) { return sign(ctx, accessID, b) },
	}
	if mimeType != "" {
		opts.QueryParameters = map[string][]string{"response-content-type": {mimeType}}
	}
	return g.client.Bucket(g.bucket).SignedURL(o.ObjectName(), opts)
}

func (g *GCS) signer(ctx context.Context) (string, SignBytesFunc, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.accessID == "" {
		email, err := serviceAccountEmail(ctx)
		if err != nil {
			return "", nil, err
		}
		g.accessID = email
	}
	if g.signBytes == nil {
		g.signBytes = iamSignBlob
	}
	return g.accessID, g.signBytes, nil
}

// serviceAccountEmail finds the identity to sign as: the metadata server on Cloud Run, else the
// email in the default credentials (a service account key or impersonation).
func serviceAccountEmail(ctx context.Context) (string, error) {
	if metadata.OnGCE() {
		if e, err := metadata.EmailWithContext(ctx, "default"); err == nil && e != "" {
			return e, nil
		}
	}
	if creds, err := google.FindDefaultCredentials(ctx); err == nil {
		var j struct {
			ClientEmail string `json:"client_email"`
		}
		if json.Unmarshal(creds.JSON, &j) == nil && j.ClientEmail != "" {
			return j.ClientEmail, nil
		}
	}
	return "", errors.New("gcs: cannot determine the service account to sign URLs as")
}

func iamSignBlob(ctx context.Context, accessID string, payload []byte) ([]byte, error) {
	svc, err := iam.NewService(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := svc.Projects.ServiceAccounts.SignBlob(
		"projects/-/serviceAccounts/"+accessID,
		&iam.SignBlobRequest{Payload: base64.StdEncoding.EncodeToString(payload)},
	).Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("gcs: iam signBlob: %w", err)
	}
	return base64.StdEncoding.DecodeString(resp.SignedBlob)
}

// gcsReader reads an object, opening a range reader lazily at the current offset. Seek just moves
// the offset and drops the open reader.
type gcsReader struct {
	ctx  context.Context
	obj  *storage.ObjectHandle
	size int64
	pos  int64
	cur  *storage.Reader
}

func (r *gcsReader) Read(p []byte) (int, error) {
	if r.pos >= r.size {
		return 0, io.EOF
	}
	if r.cur == nil {
		rd, err := r.obj.NewRangeReader(r.ctx, r.pos, -1)
		if errors.Is(err, storage.ErrObjectNotExist) {
			return 0, ErrNotFound
		}
		if err != nil {
			return 0, err
		}
		r.cur = rd
	}
	n, err := r.cur.Read(p)
	r.pos += int64(n)
	return n, err
}

func (r *gcsReader) Seek(offset int64, whence int) (int64, error) {
	var np int64
	switch whence {
	case io.SeekStart:
		np = offset
	case io.SeekCurrent:
		np = r.pos + offset
	case io.SeekEnd:
		np = r.size + offset
	default:
		return 0, errors.New("blob: bad whence")
	}
	if np < 0 {
		return 0, errors.New("blob: negative seek position")
	}
	if np != r.pos {
		r.dropReader()
		r.pos = np
	}
	return np, nil
}

func (r *gcsReader) dropReader() {
	if r.cur != nil {
		_ = r.cur.Close()
		r.cur = nil
	}
}

func (r *gcsReader) Close() error {
	r.dropReader()
	return nil
}
