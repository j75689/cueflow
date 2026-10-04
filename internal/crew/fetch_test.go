package crew

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFetcherBlocksPrivateAddresses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html><head><title>Local</title></head><body><p>secret admin page</p></body></html>"))
	}))
	defer srv.Close()

	_, err := NewFetcher(false).Fetch(context.Background(), srv.URL)
	if !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("loopback fetch must be blocked, got %v", err)
	}

	page, err := NewFetcher(true).Fetch(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if page.Title != "Local" || !strings.Contains(page.Text, "secret admin page") || strings.Contains(page.Text, "<p>") {
		t.Fatalf("page: %+v", page)
	}
}

func TestFetcherBlocksRedirectIntoPrivateNetwork(t *testing.T) {
	// A permitted-looking URL that redirects to loopback is still blocked at dial time.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://127.0.0.1:1/", http.StatusFound)
	}))
	defer srv.Close()
	f := NewFetcher(false)
	if _, err := f.Fetch(context.Background(), srv.URL); err == nil {
		t.Fatal("expected failure")
	}
}

func TestFetcherRejectsNonHTTP(t *testing.T) {
	for _, u := range []string{"file:///etc/passwd", "ftp://example.com", "/relative", "javascript:alert(1)"} {
		if _, err := NewFetcher(true).Fetch(context.Background(), u); err == nil {
			t.Errorf("%s should be rejected", u)
		}
	}
}

func TestBlockedIP(t *testing.T) {
	for ip, want := range map[string]bool{
		"127.0.0.1": true, "10.0.0.5": true, "192.168.1.1": true, "172.16.0.1": true, "169.254.169.254": true,
		"100.64.1.1": true, "::1": true, "fe80::1": true, "0.0.0.0": true,
		"1.1.1.1": false, "8.8.8.8": false, "2606:4700:4700::1111": false,
	} {
		if got := blockedIP(net.ParseIP(ip)); got != want {
			t.Errorf("%s: got %v want %v", ip, got, want)
		}
	}
}
