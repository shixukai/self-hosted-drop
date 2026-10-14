package app

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

func (a *App) trustedProxy(address netip.Addr) bool {
	for _, prefix := range a.Config.TrustedProxyCIDRs {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

func (a *App) loginClientAddress(r *http.Request) string {
	host, _, e := net.SplitHostPort(r.RemoteAddr)
	if e != nil {
		host = r.RemoteAddr
	}
	peer, e := netip.ParseAddr(host)
	if e != nil {
		return "unknown"
	}
	peer = peer.Unmap().WithZone("")
	if !a.trustedProxy(peer) {
		return peer.String()
	}
	values := r.Header.Values("X-Forwarded-For")
	length := len(values) - 1
	if len(values) == 0 || len(values) > 16 {
		return peer.String()
	}
	for _, value := range values {
		length += len(value)
	}
	if length > 4096 {
		return peer.String()
	}
	// A proxy may append a separate header field instead of extending one
	// comma-separated value. Header.Get would trust an attacker-controlled first
	// occurrence; combine every occurrence in wire order before walking back.
	forwarded := strings.Join(values, ",")
	parts := strings.Split(forwarded, ",")
	if len(parts) > 16 {
		return peer.String()
	}
	current := peer
	// Walk from the verified peer backwards. Never use a user-supplied address
	// to the left of the first untrusted hop in the proxy's appended chain.
	for i := len(parts) - 1; i >= 0 && a.trustedProxy(current); i-- {
		address, e := netip.ParseAddr(strings.TrimSpace(parts[i]))
		if e != nil || address.Zone() != "" {
			return peer.String()
		}
		current = address.Unmap()
	}
	return current.String()
}
