package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/shared"
)

const maxPageChars = 12_000

var toolSpecs = map[string]shared.FunctionDefinitionParam{
	"web_search": {
		Name:        "web_search",
		Description: openai.String("Search the web. Returns the top results with title, URL and a short snippet."),
		Parameters: shared.FunctionParameters{
			"type":       "object",
			"properties": map[string]any{"query": map[string]any{"type": "string"}},
			"required":   []string{"query"},
		},
	},
	"fetch_url": {
		Name:        "fetch_url",
		Description: openai.String("Fetch a web page and return its readable text. Use it to read a result from web_search in full."),
		Parameters: shared.FunctionParameters{
			"type":       "object",
			"properties": map[string]any{"url": map[string]any{"type": "string"}},
			"required":   []string{"url"},
		},
	},
}

// runTool never fails the chat: errors go back to the model as text so it can recover.
func runTool(ctx context.Context, c Character, name, rawArgs string) string {
	if !slices.Contains(c.Tools, name) {
		return "error: tool not available: " + name
	}

	var args struct{ Query, URL string }
	if err := json.Unmarshal([]byte(rawArgs), &args); err != nil {
		return "error: bad arguments: " + err.Error()
	}

	var out string
	var err error
	switch name {
	case "web_search":
		out, err = webSearch(ctx, args.Query)
	case "fetch_url":
		out, err = fetchURL(ctx, args.URL)
	}
	if err != nil {
		return "error: " + err.Error()
	}
	return out
}

// webSearch queries the self-hosted SearXNG. It uses the default HTTP client on purpose:
// SearXNG is configured by you and lives on localhost, which fetchClient would block.
func webSearch(ctx context.Context, query string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	base := strings.TrimRight(envOr("SEARXNG_URL", "http://127.0.0.1:8888"), "/")
	req, _ := http.NewRequestWithContext(ctx, "GET", base+"/search?format=json&q="+url.QueryEscape(query), nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("searxng unreachable (is `docker compose up -d` running?): %w", err)
	}

	defer func() {
		err := resp.Body.Close()
		if err != nil {
			return
		}
	}()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
		return "", fmt.Errorf("searxng: %s: %s", resp.Status, b)
	}

	var r struct {
		Results []struct{ Title, URL, Content string }
	}

	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return "", err
	}

	if len(r.Results) == 0 {
		return "no results", nil
	}

	var b strings.Builder
	for i, res := range r.Results[:min(len(r.Results), 8)] {
		fmt.Fprintf(&b, "%d. %s\n%s\n%s\n\n", i+1, htmlToText(res.Title), res.URL, htmlToText(res.Content))
	}

	return b.String(), nil
}

// fetchClient refuses to connect to non-public IPs. The check runs at dial time on the
// resolved address, so it also covers redirects and DNS rebinding.
var fetchClient = &http.Client{
	Timeout: 20 * time.Second,
	Transport: &http.Transport{
		Proxy:       nil, // a proxy would bypass the dial check
		DialContext: (&net.Dialer{Timeout: 10 * time.Second, Control: dialControl}).DialContext,
	},
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}

		if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
			return errors.New("redirect to non-http url")
		}

		return nil
	},
}

func dialControl(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !publicIP(ip) {
		return fmt.Errorf("blocked non-public address %s", host)
	}
	return nil
}

var blockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"), // carrier-grade NAT
	netip.MustParsePrefix("198.18.0.0/15"), // benchmarking
	netip.MustParsePrefix("64:ff9b::/96"),  // NAT64, can map to internal IPv4
}

func publicIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() {
		return false
	}
	for _, p := range blockedPrefixes {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

func fetchURL(ctx context.Context, rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", errors.New("url must be http(s)")
	}

	req, _ := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; characters/0.1)")
	resp, err := fetchClient.Do(req)
	if err != nil {
		return "", err
	}

	defer func() {
		err := resp.Body.Close()
		if err != nil {
			return
		}
	}()

	if resp.StatusCode >= 400 {
		return "", errors.New(resp.Status)
	}

	ct := resp.Header.Get("Content-Type")
	if ct != "" && !strings.HasPrefix(ct, "text/") && !strings.Contains(ct, "json") && !strings.Contains(ct, "xml") {
		return "", errors.New("unsupported content type " + ct)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 3<<20))
	if err != nil {
		return "", err
	}

	text := string(body)
	if strings.Contains(ct, "html") || ct == "" {
		text = htmlToText(text)
	}

	if len(text) > maxPageChars {
		text = strings.ToValidUTF8(text[:maxPageChars], "") + "\n[truncated]"
	}

	return "URL: " + resp.Request.URL.String() + "\n\n" + text, nil
}

var (
	reDrop   = regexp.MustCompile(`(?is)<(script|style|noscript|svg|head|template)\b.*?</(script|style|noscript|svg|head|template)>`)
	reBreak  = regexp.MustCompile(`(?i)<(br|/p|/div|/li|/h[1-6]|/tr|/section|/article|/header|/footer)\b[^>]*>`)
	reTag    = regexp.MustCompile(`(?s)<[^>]*>`)
	reSpaces = regexp.MustCompile(`[ \t\r\f\v]+`)
	reBlank  = regexp.MustCompile(`\s*\n\s*`)
)

// shortcut: regex HTML stripping, fine for reading articles; switch to x/net/html if pages come out garbled.
func htmlToText(s string) string {
	s = reDrop.ReplaceAllString(s, " ")
	s = reBreak.ReplaceAllString(s, "\n")
	s = reTag.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	s = reSpaces.ReplaceAllString(s, " ")
	s = reBlank.ReplaceAllString(s, "\n")
	return strings.TrimSpace(s)
}
