package web

import "testing"

func TestMalformedAccessURLReturnsError(t *testing.T) {
	for _, origin := range []string{"https://%", "https://[", "https://user:password@example.test", "https://example.test/path"} {
		if _, err := New(&fakeBackend{}, Options{AccessURL: origin}); err == nil {
			t.Fatalf("invalid origin accepted: %s", origin)
		}
	}
}
