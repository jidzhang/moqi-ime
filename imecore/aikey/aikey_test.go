package aikey

import (
	"strings"
	"testing"
)

func TestMaskResolveRoundTrip(t *testing.T) {
	// 内置 key 不保证以 sk- 开头，也不保证是 ASCII。
	for _, key := range []string{"sk-round-trip", "plain-token", "含中文的密钥", "a"} {
		masked := Mask(key)
		if !strings.HasPrefix(masked, Prefix) {
			t.Fatalf("masked value %q must carry prefix %q", masked, Prefix)
		}
		if len(key) > 3 && strings.Contains(masked, key) {
			t.Fatalf("masked value %q must not leak plaintext %q", masked, key)
		}
		if got := Resolve(masked); got != key {
			t.Fatalf("resolve(%q) = %q, want %q", masked, got, key)
		}
		if !IsMasked(masked) {
			t.Fatalf("%q must be recognized as masked", masked)
		}
	}
}

func TestResolveKeepsUserPlaintext(t *testing.T) {
	for _, raw := range []string{"sk-user-own-key", "abc123token", "别家的密钥", "!!not-base64!!", "", "MQ-"} {
		if got := Resolve(raw); got != raw {
			t.Fatalf("plaintext value %q must pass through, got %q", raw, got)
		}
		if IsMasked(raw) {
			t.Fatalf("%q must not be treated as masked", raw)
		}
	}
}

func TestResolveRejectsCorruptedTag(t *testing.T) {
	masked := Mask("sk-round-trip")
	corrupted := masked[:len(masked)-4] + "AAAA"
	if got := Resolve(corrupted); got != corrupted {
		t.Fatalf("corrupted value must pass through unchanged, got %q", got)
	}
	if IsMasked(corrupted) {
		t.Fatal("corrupted value must not be treated as masked")
	}
}
