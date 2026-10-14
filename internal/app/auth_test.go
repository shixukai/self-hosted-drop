package app

import (
	"github.com/shixukai/self-hosted-drop/internal/config"
	"net/http/httptest"
	"testing"
	"time"
)

func TestPasswordHash(t *testing.T) {
	hash := passwordHash("a long test password")
	if !passwordOK("a long test password", hash) || passwordOK("wrong password", hash) || passwordOK("any", "malformed") {
		t.Fatal("password verification failed")
	}
}
func TestProductionCookieIsolation(t *testing.T) {
	a := &App{Config: config.Config{Development: false}}
	w := httptest.NewRecorder()
	a.cookie(w, a.cookieName(false), "test-session", time.Now().Add(time.Hour))
	c := w.Result().Cookies()[0]
	if c.Name != "__Host-drop_session" || !c.Secure || !c.HttpOnly || c.Path != "/" || c.Domain != "" {
		t.Fatalf("unsafe cookie attributes: %+v", c)
	}
	r := httptest.NewRequest("POST", "https://drop-admin.example.com/api/v1/sites", nil)
	a.Config.AdminOrigin = "https://drop-admin.example.com"
	r.Header.Set("Origin", "https://evil.drop.example.com")
	if a.origin(r) == nil {
		t.Fatal("content origin accepted")
	}
	r.Header.Set("Origin", a.Config.AdminOrigin)
	if a.origin(r) != nil {
		t.Fatal("admin origin rejected")
	}
}
func TestArgonConcurrencyBound(t *testing.T) {
	a := &App{authGate: make(chan struct{}, 2)}
	one, e := a.authSlot()
	if e != nil {
		t.Fatal(e)
	}
	defer one()
	two, e := a.authSlot()
	if e != nil {
		t.Fatal(e)
	}
	defer two()
	if _, e = a.authSlot(); e == nil {
		t.Fatal("unbounded password hashing")
	}
}
