package config

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	DatabaseURL, DataDir, WebDir, AdminOrigin, ContentDomain, PublicScheme                                               string
	Listen, InternalListen, CaddyAdmin, CaddyContent, CaddyProbe, CaddyRoot, WorkerURL, InternalKey, WorkerKey, SetupKey string
	Development                                                                                                          bool
	MaxUpload, MaxExpanded, MaxFile, Quota, DiskReserve                                                                  int64
	MaxFiles, MaxDepth, QueueLimit, Concurrency, RetainVersions, TrashDays                                               int
	ProcessTimeout                                                                                                       time.Duration
	TrustedProxyCIDRs                                                                                                    []netip.Prefix
}

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
func number(k string, d int64) int64 {
	v := os.Getenv(k)
	if v == "" {
		return d
	}
	n, e := strconv.ParseInt(v, 10, 64)
	if e != nil || n < 1 {
		panic("invalid positive integer: " + k)
	}
	return n
}
func secret(k string) (string, error) {
	if p := os.Getenv(k + "_FILE"); p != "" {
		b, e := os.ReadFile(p)
		return strings.TrimSpace(string(b)), e
	}
	return os.Getenv(k), nil
}
func Load() (Config, error) {
	c := Config{DatabaseURL: os.Getenv("DROP_DATABASE_URL"), DataDir: env("DROP_DATA_DIR", ".data"), WebDir: env("DROP_WEB_DIR", "web/dist"), AdminOrigin: env("DROP_ADMIN_ORIGIN", "https://drop-admin.douyacn.com"), ContentDomain: env("DROP_CONTENT_DOMAIN", "drop.douyacn.com"), PublicScheme: "https", Listen: env("DROP_LISTEN", "127.0.0.1:18180"), InternalListen: env("DROP_INTERNAL_LISTEN", "127.0.0.1:18181"), CaddyAdmin: env("DROP_CADDY_ADMIN", "http://127.0.0.1:2019"), CaddyContent: env("DROP_CADDY_CONTENT", "http://127.0.0.1:18080"), WorkerURL: env("DROP_WORKER_URL", "http://127.0.0.1:18182"), Development: os.Getenv("DROP_DEVELOPMENT_MODE") == "true", MaxUpload: number("DROP_MAX_UPLOAD_BYTES", 200<<20), MaxExpanded: number("DROP_MAX_EXPANDED_BYTES", 1<<30), MaxFile: number("DROP_MAX_FILE_BYTES", 100<<20), Quota: number("DROP_QUOTA_BYTES", 20<<30), DiskReserve: number("DROP_DISK_RESERVE_BYTES", 5<<30), MaxFiles: int(number("DROP_MAX_FILES", 10000)), MaxDepth: 20, QueueLimit: 20, Concurrency: 2, RetainVersions: 10, TrashDays: 7, ProcessTimeout: 120 * time.Second}
	var err error
	c.TrustedProxyCIDRs, err = ParseTrustedProxies(os.Getenv("DROP_TRUSTED_PROXY_CIDRS"))
	if err != nil {
		return c, err
	}
	c.DataDir, err = filepath.Abs(c.DataDir)
	if err != nil {
		return c, err
	}
	c.CaddyProbe = env("DROP_CADDY_PROBE", "http://127.0.0.1:18081")
	c.CaddyRoot = env("DROP_CADDY_ROOT", filepath.Join(c.DataDir, "releases"))
	c.SetupKey, err = secret("DROP_SETUP_KEY")
	if err != nil {
		return c, err
	}
	c.InternalKey, err = secret("DROP_INTERNAL_KEY")
	if err != nil {
		return c, err
	}
	c.WorkerKey, err = secret("DROP_WORKER_KEY")
	if err != nil {
		return c, err
	}
	if c.WorkerKey == "" && c.Development {
		c.WorkerKey = c.InternalKey
	}
	if len(c.WorkerKey) < 32 {
		return c, errors.New("DROP_WORKER_KEY_FILE must contain at least 32 characters")
	}
	if len(c.InternalKey) < 32 {
		return c, errors.New("DROP_INTERNAL_KEY_FILE must contain at least 32 characters")
	}
	u, err := url.Parse(c.AdminOrigin)
	if err != nil || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return c, errors.New("invalid DROP_ADMIN_ORIGIN")
	}
	contentHostname := c.ContentDomain
	if c.Development {
		if !localHost(u.Hostname()) || !localHost(c.ContentDomain) {
			return c, errors.New("development origins must use loopback or .localhost")
		}
		c.PublicScheme = "http"
	} else {
		if u.Scheme != "https" {
			return c, errors.New("production admin origin must use https")
		}
		c.ContentDomain, contentHostname, err = parseProductionContentDomain(c.ContentDomain)
		if err != nil {
			return c, err
		}
	}
	adminHostname := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	contentHostname = strings.ToLower(contentHostname)
	if adminHostname == contentHostname || strings.HasSuffix(adminHostname, "."+contentHostname) {
		return c, errors.New("admin and content origins must be separate")
	}
	if c.DatabaseURL == "" && (os.Getenv("DROP_POSTGRES_PASSWORD_FILE") != "" || os.Getenv("DROP_POSTGRES_PASSWORD") != "") {
		password, e := secret("DROP_POSTGRES_PASSWORD")
		if e != nil {
			return c, e
		}
		u := url.URL{Scheme: "postgres", Host: env("DROP_POSTGRES_HOST", "db:5432"), Path: env("DROP_POSTGRES_DB", "drop"), User: url.UserPassword(env("DROP_POSTGRES_USER", "drop"), password), RawQuery: "sslmode=disable"}
		c.DatabaseURL = u.String()
	}
	if c.DatabaseURL == "" {
		return c, errors.New("DROP_DATABASE_URL is required")
	}
	for _, d := range []string{"uploads", "quarantine", "releases", "runtime"} {
		if err = os.MkdirAll(filepath.Join(c.DataDir, d), 0750); err != nil {
			return c, fmt.Errorf("prepare storage: %w", err)
		}
	}
	return c, nil
}

// A content domain is a DNS suffix used to construct every published host. The
// optional port describes the external HTTPS listener, not a part of the suffix.
func parseProductionContentDomain(value string) (authority, hostname string, err error) {
	invalid := errors.New("DROP_CONTENT_DOMAIN must be a DNS hostname with an optional port from 1 to 65535")
	u, e := url.Parse("//" + value)
	if e != nil || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.ContainsAny(value, "/?#@") {
		return "", "", invalid
	}
	hostname = strings.ToLower(u.Hostname())
	if hostname == "" || len(hostname) > 253 || net.ParseIP(hostname) != nil {
		return "", "", invalid
	}
	labels := strings.Split(hostname, ".")
	for _, label := range labels {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", "", invalid
		}
		for _, ch := range label {
			if !(ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' || ch == '-') {
				return "", "", invalid
			}
		}
	}
	// Browsers also interpret shortened, integer, and hexadecimal IPv4 forms as
	// addresses. A numeric final label must not enter the published DNS suffix.
	last := labels[len(labels)-1]
	numeric := true
	for _, ch := range last {
		if ch < '0' || ch > '9' {
			numeric = false
		}
	}
	if numeric {
		return "", "", invalid
	}
	if strings.HasPrefix(last, "0x") && len(last) > 2 {
		numeric = true
		for _, ch := range last[2:] {
			if !(ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'f') {
				numeric = false
			}
		}
		if numeric {
			return "", "", invalid
		}
	}
	authority = hostname
	if strings.Contains(value, ":") {
		port := u.Port()
		if port == "" || strings.Count(value, ":") != 1 {
			return "", "", invalid
		}
		for _, ch := range port {
			if ch < '0' || ch > '9' {
				return "", "", invalid
			}
		}
		n, e := strconv.ParseUint(port, 10, 16)
		if e != nil || n == 0 {
			return "", "", invalid
		}
		// The browser omits the default HTTPS port from Origin; preview exchange
		// compares that origin with SiteURL, so use the same canonical form.
		if n != 443 {
			authority += ":" + strconv.FormatUint(n, 10)
		}
	}
	return authority, hostname, nil
}

func ParseTrustedProxies(value string) ([]netip.Prefix, error) {
	trusted := []netip.Prefix{}
	if strings.TrimSpace(value) == "" {
		return trusted, nil
	}
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		prefix, e := netip.ParsePrefix(part)
		if e != nil {
			address, err := netip.ParseAddr(part)
			if err != nil || address.Zone() != "" {
				return nil, errors.New("invalid DROP_TRUSTED_PROXY_CIDRS")
			}
			address = address.Unmap()
			prefix = netip.PrefixFrom(address, address.BitLen())
		}
		if prefix.Bits() == 0 {
			return nil, errors.New("DROP_TRUSTED_PROXY_CIDRS cannot trust every address")
		}
		trusted = append(trusted, prefix.Masked())
	}
	return trusted, nil
}
func localHost(h string) bool {
	if i := strings.IndexByte(h, ':'); i >= 0 {
		h = h[:i]
	}
	return h == "localhost" || strings.HasSuffix(h, ".localhost") || h == "127.0.0.1" || h == "::1"
}
func (c Config) SiteURL(slug string) string {
	return c.PublicScheme + "://" + slug + "." + c.ContentDomain
}
