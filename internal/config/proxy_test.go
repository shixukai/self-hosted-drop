package config

import "testing"

func TestParseTrustedProxies(t *testing.T) {
	for _, input := range []string{"", "127.0.0.1", "10.0.0.0/24, ::1/128"} {
		if _, e := ParseTrustedProxies(input); e != nil {
			t.Fatalf("valid list rejected %q: %v", input, e)
		}
	}
	for _, input := range []string{"0.0.0.0/0", "::/0", "bad", "127.0.0.1,", "127.0.0.1/33", "[::1]"} {
		if _, e := ParseTrustedProxies(input); e == nil {
			t.Fatalf("invalid list accepted %q", input)
		}
	}
}
