package config

import (
	"strings"
	"testing"
)

func TestProductionContentDomain(t *testing.T) {
	for _, tc := range []struct {
		input, authority, hostname string
	}{
		{"drop.douyacn.com", "drop.douyacn.com", "drop.douyacn.com"},
		{"DROP.DOUYACN.COM:10844", "drop.douyacn.com:10844", "drop.douyacn.com"},
		{"drop.douyacn.com:1", "drop.douyacn.com:1", "drop.douyacn.com"},
		{"drop.douyacn.com:65535", "drop.douyacn.com:65535", "drop.douyacn.com"},
		{"drop.douyacn.com:010844", "drop.douyacn.com:10844", "drop.douyacn.com"},
		{"drop.douyacn.com:443", "drop.douyacn.com", "drop.douyacn.com"},
		{"xn--bcher-kva.example", "xn--bcher-kva.example", "xn--bcher-kva.example"},
	} {
		t.Run(tc.input, func(t *testing.T) {
			authority, hostname, err := parseProductionContentDomain(tc.input)
			if err != nil || authority != tc.authority || hostname != tc.hostname {
				t.Fatalf("parse(%q) = %q, %q, %v", tc.input, authority, hostname, err)
			}
		})
	}
	for _, input := range []string{
		"", "https://drop.douyacn.com", "//drop.douyacn.com", "user@drop.douyacn.com",
		"drop.douyacn.com/", "drop.douyacn.com/path", "drop.douyacn.com?query", "drop.douyacn.com?",
		"drop.douyacn.com#fragment", "drop.douyacn.com#", "drop.douyacn.com:", "drop.douyacn.com:0",
		"drop.douyacn.com:65536", "drop.douyacn.com:-1", "drop.douyacn.com:+443", "drop.douyacn.com:bad",
		"drop.douyacn.com:443:443", "drop.douyacn.com:443/path", "drop.douyacn.com:443?query",
		"127.0.0.1", "127.0.0.1:10844", "[::1]", "[::1]:10844", "::1",
		"127.1", "2130706433", "0x7f000001", "0x7f.0.0.1", "1.2.3.0x1",
		"drop.douyacn.com.", ".drop.douyacn.com", "drop..douyacn.com", "-drop.douyacn.com",
		"drop-.douyacn.com", "drop_domain.example", "drop.douyacn.com ", " drop.douyacn.com",
		"[drop.douyacn.com]", "döp.example", "drop%2Edouyacn.com", strings.Repeat("a", 64) + ".example",
		strings.Repeat(strings.Repeat("a", 63)+".", 4) + "example",
	} {
		t.Run(input, func(t *testing.T) {
			if _, _, err := parseProductionContentDomain(input); err == nil {
				t.Fatalf("invalid content domain accepted: %q", input)
			}
		})
	}
}

func configureDomainTest(t *testing.T, admin, content string, development bool) {
	t.Helper()
	t.Setenv("DROP_DATABASE_URL", "postgres://drop:test-only@localhost/drop")
	t.Setenv("DROP_DATA_DIR", t.TempDir())
	t.Setenv("DROP_ADMIN_ORIGIN", admin)
	t.Setenv("DROP_CONTENT_DOMAIN", content)
	t.Setenv("DROP_TRUSTED_PROXY_CIDRS", "")
	t.Setenv("DROP_INTERNAL_KEY_FILE", "")
	t.Setenv("DROP_WORKER_KEY_FILE", "")
	t.Setenv("DROP_SETUP_KEY_FILE", "")
	t.Setenv("DROP_INTERNAL_KEY", strings.Repeat("i", 32))
	t.Setenv("DROP_WORKER_KEY", strings.Repeat("w", 32))
	t.Setenv("DROP_SETUP_KEY", "")
	if development {
		t.Setenv("DROP_DEVELOPMENT_MODE", "true")
	} else {
		t.Setenv("DROP_DEVELOPMENT_MODE", "false")
	}
}

func TestLoadProductionContentPort(t *testing.T) {
	configureDomainTest(t, "https://drop-admin.douyacn.com:10844", "DROP.DOUYACN.COM:10844", false)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.ContentDomain != "drop.douyacn.com:10844" || c.SiteURL("published") != "https://published.drop.douyacn.com:10844" || c.SiteURL("d-preview") != "https://d-preview.drop.douyacn.com:10844" {
		t.Fatalf("external content port not preserved in site and preview URLs: %#v", c)
	}
}

func TestLoadProductionOriginIsolationIgnoresPorts(t *testing.T) {
	for _, admin := range []string{
		"https://drop.douyacn.com", "https://drop.douyacn.com:443", "https://drop.douyacn.com:10845",
		"https://admin.drop.douyacn.com:10845", "https://ADMIN.DROP.DOUYACN.COM:10845", "https://drop.douyacn.com.:10845",
	} {
		t.Run(admin, func(t *testing.T) {
			configureDomainTest(t, admin, "drop.douyacn.com:10844", false)
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), "must be separate") {
				t.Fatalf("shared content suffix accepted with different ports: %q, %v", admin, err)
			}
		})
	}
}

func TestLoadDevelopmentOriginRestriction(t *testing.T) {
	for _, tc := range []struct {
		admin, content string
		allowed        bool
	}{
		{"http://localhost:18180", "drop.localhost:18080", true},
		{"http://127.0.0.1:18180", "drop.localhost:18080", true},
		{"http://drop-admin.douyacn.com:18180", "drop.localhost:18080", false},
		{"http://localhost:18180", "drop.douyacn.com:10844", false},
		{"http://localhost:18180", "drop.localhost.evil.example:10844", false},
	} {
		t.Run(tc.admin+"/"+tc.content, func(t *testing.T) {
			configureDomainTest(t, tc.admin, tc.content, true)
			c, err := Load()
			if (err == nil) != tc.allowed {
				t.Fatalf("development origin policy: allowed=%v, err=%v", tc.allowed, err)
			}
			if tc.allowed && (c.PublicScheme != "http" || c.SiteURL("site") != "http://site."+tc.content) {
				t.Fatalf("development URL changed: %q", c.SiteURL("site"))
			}
		})
	}
}
