package tools

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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("secret")) }))
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

func TestRegistryValidation(t *testing.T) {
	r := New()
	ctx := context.Background()
	env := Env{Allowed: []string{"web_search", "fetch_url"}}

	cases := []struct {
		name string
		env  Env
		tool string
		args string
		want string
	}{
		{name: "unknown", env: env, tool: "nope", args: `{}`, want: "tool not available: nope"},
		{name: "not allowed", env: Env{Allowed: []string{"fetch_url"}}, tool: "web_search", args: `{"query":"x"}`, want: "tool not available: web_search"},
		{name: "bad json", env: env, tool: "web_search", args: `{`, want: "bad arguments"},
		{name: "empty query", env: env, tool: "web_search", args: `{"query":"  "}`, want: "query is required"},
		{name: "empty url", env: env, tool: "fetch_url", args: `{}`, want: "url is required"},
		{name: "file url", env: env, tool: "fetch_url", args: `{"url":"file:///etc/passwd"}`, want: "url must be http(s)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := r.Run(ctx, tc.env, tc.tool, tc.args)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}

	specs := r.Specs([]string{"fetch_url", "web_search", "nope"})
	if len(specs) != 2 || specs[0].Name != "fetch_url" || specs[1].Name != "web_search" {
		t.Fatalf("specs: %+v", specs)
	}
	if !strings.Contains(string(specs[1].Parameters), `"query"`) {
		t.Fatalf("web_search schema: %s", specs[1].Parameters)
	}
}

func TestWebSearch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("q") != "go" || r.URL.Query().Get("format") != "json" {
			t.Errorf("query: %s", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"title":"Go","url":"https://go.dev","content":"The Go language"}]}`))
	}))
	defer srv.Close()
	t.Setenv("SEARXNG_URL", srv.URL)

	out, err := New().Run(context.Background(), Env{Allowed: []string{"web_search"}}, "web_search", `{"query":"go"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "1. Go") || !strings.Contains(out, "https://go.dev") || !strings.Contains(out, "The Go language") {
		t.Fatalf("search output: %q", out)
	}
}
