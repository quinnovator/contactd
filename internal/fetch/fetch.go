// Package fetch reads bounded web resources with address checks at dial time.
package fetch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/html"

	"github.com/quinnovator/contactd/internal/turn"
)

// Config bounds outbound reads. Allow lists exact host:port exceptions for private addresses.
type (
	Config struct {
		Allow     []string
		Timeout   time.Duration
		MaxBytes  int64
		UserAgent string
	}
	// Fetcher owns its transport; environment proxies cannot bypass its address checks.
	Fetcher struct {
		client *http.Client
		cfg    Config
	}
)

// New constructs a fetcher. The zero configuration selects conservative limits.
func New(cfg Config) *Fetcher {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 15 * time.Second
	}
	if cfg.MaxBytes <= 0 {
		cfg.MaxBytes = 2 << 20
	}
	if cfg.UserAgent == "" {
		cfg.UserAgent = "contactd-helper/1 (+https://github.com/quinnovator/contactd)"
	}
	cfg.Allow = slices.Clone(cfg.Allow)
	f := &Fetcher{cfg: cfg}
	f.client = &http.Client{Timeout: cfg.Timeout, Transport: &http.Transport{DialContext: f.dial, MaxIdleConns: 16, IdleConnTimeout: time.Minute}, CheckRedirect: func(r *http.Request, via []*http.Request) error {
		if len(via) > 5 {
			return errors.New("too many redirects")
		}
		return validURL(r.URL)
	}}
	return f
}

func validURL(u *url.URL) error {
	if (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
		return errors.New("expected an absolute http or https URL without credentials")
	}
	return nil
}

func (f *Fetcher) dial(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("dial address: %w", err)
	}
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, fmt.Errorf("resolve fetch host: %w", err)
	}
	allow := slices.Contains(f.cfg.Allow, net.JoinHostPort(host, port))
	for _, ip := range ips {
		if !allow && !public(ip) {
			return nil, errors.New("fetch address is private or reserved")
		}
	}
	var last error
	for _, ip := range ips {
		var d net.Dialer
		conn, err := d.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
		last = err
	}
	if last == nil {
		last = errors.New("host has no addresses")
	}
	return nil, fmt.Errorf("dial fetch host: %w", last)
}

func public(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() || ip.IsMulticast() {
		return false
	}
	for _, p := range reserved() {
		if netip.MustParsePrefix(p).Contains(ip) {
			return false
		}
	}
	return true
}

// reserved lists special-purpose ranges that IsGlobalUnicast accepts but that
// are not the public internet. Shared address space is where carrier NAT and
// Tailscale live; the IPv6 translation and tunnel prefixes embed IPv4
// addresses, private ones included.
func reserved() []string {
	return []string{
		"0.0.0.0/8",       // this network
		"100.64.0.0/10",   // shared address space (CGNAT, Tailscale)
		"192.0.0.0/24",    // IETF protocol assignments
		"192.0.2.0/24",    // documentation
		"192.88.99.0/24",  // 6to4 relay anycast
		"198.18.0.0/15",   // benchmarking
		"198.51.100.0/24", // documentation
		"203.0.113.0/24",  // documentation
		"240.0.0.0/4",     // reserved, and broadcast
		"::/96",           // IPv4-compatible
		"64:ff9b::/96",    // NAT64
		"64:ff9b:1::/48",  // local NAT64
		"100::/64",        // discard
		"2001::/32",       // Teredo
		"2001:2::/48",     // benchmarking
		"2001:db8::/32",   // documentation
		"2002::/16",       // 6to4
		"fec0::/10",       // site-local
	}
}

// Fetch reads at most MaxBytes and reduces HTML to visible text.
func (f *Fetcher) Fetch(ctx context.Context, rawURL string) (turn.Page, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return turn.Page{}, fmt.Errorf("parse fetch URL: %w", err)
	}
	if err := validURL(u); err != nil {
		return turn.Page{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return turn.Page{}, fmt.Errorf("create fetch request: %w", err)
	}
	req.Header.Set("User-Agent", f.cfg.UserAgent)
	req.Header.Set("Accept", "text/html, text/plain, application/json;q=0.9, */*;q=0.1")
	resp, err := f.client.Do(req)
	if err != nil {
		return turn.Page{}, fmt.Errorf("fetch page: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, f.cfg.MaxBytes+1))
	if err != nil {
		return turn.Page{}, fmt.Errorf("read page: %w", err)
	}
	cut := int64(len(raw)) > f.cfg.MaxBytes
	if cut {
		raw = raw[:f.cfg.MaxBytes]
	}
	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = http.DetectContentType(raw)
	}
	kind, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return turn.Page{}, fmt.Errorf("parse content type: %w", err)
	}
	page := turn.Page{URL: resp.Request.URL.String(), Status: resp.StatusCode, Type: kind}
	switch {
	case kind == "text/html":
		root, err := html.Parse(strings.NewReader(string(raw)))
		if err != nil {
			return turn.Page{}, fmt.Errorf("parse page: %w", err)
		}
		page.Title, page.Text = htmlText(root)
	case strings.HasPrefix(kind, "text/"), kind == "application/json":
		page.Text = string(raw)
	default:
		return turn.Page{}, fmt.Errorf("unsupported content type %s", kind)
	}
	if cut {
		page.Text += "\n[… page cut at " + strconv.FormatInt(f.cfg.MaxBytes, 10) + " bytes …]"
	}
	return page, nil
}

func htmlText(root *html.Node) (string, string) {
	var title, text strings.Builder
	var walk func(*html.Node, bool, bool)
	walk = func(n *html.Node, inHead, inTitle bool) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "script", "style", "noscript", "template", "svg":
				return
			case "head":
				inHead = true
			case "title":
				inTitle = true
			}
		}
		line := n.Type == html.ElementNode && slices.Contains([]string{"br", "p", "div", "li", "tr", "h1", "h2", "h3", "h4", "h5", "h6", "section"}, n.Data)
		if line {
			text.WriteByte('\n')
		}
		if n.Type == html.TextNode {
			if inTitle {
				title.WriteString(n.Data)
			} else if !inHead {
				text.WriteString(n.Data)
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c, inHead, inTitle)
		}
		if line {
			text.WriteByte('\n')
		}
	}
	walk(root, false, false)
	var lines []string
	for line := range strings.SplitSeq(text.String(), "\n") {
		if clean := strings.Join(strings.Fields(line), " "); clean != "" {
			lines = append(lines, clean)
		}
	}
	return strings.Join(strings.Fields(title.String()), " "), strings.Join(lines, "\n")
}
