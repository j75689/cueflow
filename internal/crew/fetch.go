package crew

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/j75689/cueflow/internal/connectors/rss"
)

// Fetcher reads web pages for agents. By default it refuses private, loopback and link-local
// addresses (checked on the resolved IP at dial time, so DNS tricks and redirects are covered):
// an agent must not be steerable into the operator's LAN, a publisher's admin API, or cloud metadata.
type Fetcher struct {
	client   *http.Client
	maxBytes int64
	maxChars int
}

var ErrBlockedAddress = errors.New("address not allowed (private or local network)")

func NewFetcher(allowPrivate bool) *Fetcher {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	if !allowPrivate {
		dialer.Control = func(network, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			ip := net.ParseIP(host)
			if ip == nil || blockedIP(ip) {
				return ErrBlockedAddress
			}
			return nil
		}
	}
	transport := &http.Transport{
		DialContext:           dialer.DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 20 * time.Second,
		MaxIdleConns:          10,
		Proxy:                 nil, // never route agent fetches through an operator proxy
	}
	return &Fetcher{
		client: &http.Client{
			Transport: transport,
			Timeout:   30 * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 5 {
					return errors.New("too many redirects")
				}
				if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
					return errors.New("redirect to unsupported scheme")
				}
				return nil
			},
		},
		maxBytes: 2 << 20,
		maxChars: 8000,
	}
}

var cgnat = mustCIDR("100.64.0.0/10")

func mustCIDR(s string) *net.IPNet {
	_, n, _ := net.ParseCIDR(s)
	return n
}

func blockedIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() || cgnat.Contains(ip)
}

var titleRe = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)

type Page struct {
	URL   string `json:"url"`
	Title string `json:"title"`
	Text  string `json:"text"`
}

func (f *Fetcher) Fetch(ctx context.Context, raw string) (*Page, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("only absolute http(s) URLs can be read")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "CueFlow-Agent/0.1 (+https://github.com/j75689/cueflow)")
	req.Header.Set("Accept", "text/html,text/plain,application/json;q=0.9,*/*;q=0.5")
	resp, err := f.client.Do(req)
	if err != nil {
		if errors.Is(err, ErrBlockedAddress) {
			return nil, ErrBlockedAddress
		}
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	if ct != "" && !strings.Contains(ct, "text/") && !strings.Contains(ct, "json") && !strings.Contains(ct, "xml") {
		return nil, fmt.Errorf("unsupported content type %q", ct)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, f.maxBytes))
	if err != nil {
		return nil, err
	}
	page := &Page{URL: resp.Request.URL.String()}
	html := string(body)
	if m := titleRe.FindStringSubmatch(html); m != nil {
		page.Title = strings.TrimSpace(rss.StripHTML(m[1]))
	}
	text := html
	if strings.Contains(ct, "html") || strings.Contains(strings.ToLower(html[:min(len(html), 512)]), "<html") {
		text = rss.StripHTML(html)
	}
	if r := []rune(text); len(r) > f.maxChars {
		text = string(r[:f.maxChars]) + "…"
	}
	page.Text = text
	return page, nil
}
