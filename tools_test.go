package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func TestPublicIP(t *testing.T) {
	for ip, want := range map[string]bool{
		"8.8.8.8":          true,
		"2606:4700::1111":  true,
		"127.0.0.1":        false,
		"10.1.2.3":         false,
		"172.16.0.1":       false,
		"192.168.1.1":      false,
		"169.254.169.254":  false, // cloud metadata
		"100.64.0.1":       false,
		"0.0.0.0":          false,
		"::1":              false,
		"fd00::1":          false,
		"fe80::1":          false,
		"::ffff:127.0.0.1": false,
		"64:ff9b::a00:1":   false,
	} {
		if got := publicIP(netip.MustParseAddr(ip)); got != want {
			t.Errorf("publicIP(%s) = %v, want %v", ip, got, want)
		}
	}
}

func TestFetchURLBlocksLocalhost(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("secret")) }))
	defer srv.Close()
	if out, err := fetchURL(context.Background(), srv.URL); err == nil || strings.Contains(out, "secret") {
		t.Fatalf("expected localhost to be blocked, got %q, %v", out, err)
	}
	if _, err := fetchURL(context.Background(), "file:///etc/passwd"); err == nil {
		t.Fatal("expected file:// to be rejected")
	}
}

func TestHTMLToText(t *testing.T) {
	in := `<html><head><title>x</title></head><body><script>evil()</script><h1>Hi &amp; bye</h1><p>one</p><p>two   three</p></body></html>`
	if got, want := htmlToText(in), "Hi & bye\none\ntwo three"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
