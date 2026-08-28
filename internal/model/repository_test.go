package model

import "testing"

func TestSanitizeRemoteURL(t *testing.T) {
	tests := map[string]string{
		"https://user:secret@example.com/owner/repo.git?access_token=secret#fragment": "https://example.com/owner/repo.git",
		"https://token@example.com/owner/repo.git":                                    "https://example.com/owner/repo.git",
		"ssh://git@example.com/owner/repo.git":                                        "ssh://git@example.com/owner/repo.git",
		"git@example.com:owner/repo.git":                                              "git@example.com:owner/repo.git",
		"":                                                                            "",
	}
	for input, want := range tests {
		got, err := SanitizeRemoteURL(input)
		if err != nil || got != want {
			t.Errorf("SanitizeRemoteURL(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
	if _, err := SanitizeRemoteURL("https://example.com/repo.git\nmalicious"); err == nil {
		t.Fatal("expected control-character rejection")
	}
}
