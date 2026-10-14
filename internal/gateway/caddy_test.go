package gateway

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestUnknownHostsHaveNoFallbackRoot(t *testing.T) {
	cfg := Build(Options{AdminListen: "127.0.0.1:2019", Routes: []Route{{ID: "site-x-g1", Host: "known.example.com", Root: "/releases/x/public", Entry: "report.html"}}})
	if !HasRoute(cfg, "site-x-g1") || HasRoute(cfg, "site-y-g1") {
		t.Fatal("route identification failed")
	}
	b, e := json.Marshal(cfg)
	if e != nil {
		t.Fatal(e)
	}
	s := string(b)
	if !strings.Contains(s, `"automatic_https":{"disable":true}`) || !strings.Contains(s, `"status_code":404`) || !strings.Contains(s, `"canonical_uris":false`) {
		t.Fatalf("unsafe gateway defaults")
	}
}
