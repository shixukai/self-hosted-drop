package app

import (
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/shixukai/self-hosted-drop/internal/config"
)

func TestTrustedProxyClientAddress(t *testing.T) {
	trusted, e := config.ParseTrustedProxies("127.0.0.1/32,10.0.0.0/8,::1/128")
	if e != nil {
		t.Fatal(e)
	}
	a := &App{Config: config.Config{TrustedProxyCIDRs: trusted}}
	for _, c := range []struct{ peer, forwarded, want string }{
		{"198.51.100.1:1234", "203.0.113.2", "198.51.100.1"},
		{"127.0.0.1:1234", "203.0.113.2", "203.0.113.2"},
		{"127.0.0.1:1234", "203.0.113.99,198.51.100.2,10.0.0.2", "198.51.100.2"},
		{"[::1]:1234", "2001:db8::2", "2001:db8::2"},
		{"[::ffff:127.0.0.1]:1234", "203.0.113.2", "203.0.113.2"},
		{"127.0.0.1:1234", "not-an-address", "127.0.0.1"},
		{"127.0.0.1:1234", "203.0.113.2,unknown", "127.0.0.1"},
	} {
		r := httptest.NewRequest("POST", "http://localhost/login", nil)
		r.RemoteAddr = c.peer
		r.Header.Set("X-Forwarded-For", c.forwarded)
		if got := a.loginClientAddress(r); got != c.want {
			t.Fatalf("peer=%q chain=%q got=%q want=%q", c.peer, c.forwarded, got, c.want)
		}
	}
	r := httptest.NewRequest("POST", "http://localhost/login", nil)
	r.RemoteAddr = "127.0.0.1:1234"
	r.Header.Add("X-Forwarded-For", "203.0.113.99")
	r.Header.Add("X-Forwarded-For", "198.51.100.1")
	if got := a.loginClientAddress(r); got != "198.51.100.1" {
		t.Fatalf("spoofed first forwarded field accepted: %s", got)
	}
}

func TestProxyLoginRateBuckets(t *testing.T) {
	trusted, _ := config.ParseTrustedProxies("127.0.0.1")
	a := &App{Config: config.Config{TrustedProxyCIDRs: trusted}, rates: map[string]rateBucket{}}
	request := func(client string) error {
		r := httptest.NewRequest("POST", "http://localhost/login", nil)
		r.RemoteAddr = "127.0.0.1:1234"
		r.Header.Set("X-Forwarded-For", client)
		return a.loginRate(r)
	}
	for i := 0; i < 10; i++ {
		if e := request("203.0.113.1"); e != nil {
			t.Fatal(e)
		}
	}
	if request("203.0.113.1") == nil {
		t.Fatal("exhausted client accepted")
	}
	if e := request("203.0.113.2"); e != nil {
		t.Fatalf("different client shared proxy bucket: %v", e)
	}
	for i := 3; i <= 21; i++ {
		if e := request(fmt.Sprintf("203.0.113.%d", i)); e != nil {
			t.Fatal(e)
		}
	}
	if request("203.0.113.22") == nil {
		t.Fatal("global resource ceiling was removed")
	}
	// Unconfigured forwarding headers are never accepted as identity.
	b := &App{rates: map[string]rateBucket{}}
	for i := 0; i < 10; i++ {
		r := httptest.NewRequest("POST", "http://localhost/login", nil)
		r.RemoteAddr = "198.51.100.1:1234"
		r.Header.Set("X-Forwarded-For", "203.0.113.1")
		if e := b.loginRate(r); e != nil {
			t.Fatal(e)
		}
	}
	r := httptest.NewRequest("POST", "http://localhost/login", nil)
	r.RemoteAddr = "198.51.100.1:1234"
	r.Header.Set("X-Forwarded-For", "203.0.113.2")
	if b.loginRate(r) == nil {
		t.Fatal("untrusted forwarded address bypassed limit")
	}
}
