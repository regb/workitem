package cli

import (
	"strings"
	"testing"
)

// The operator lists, grants and revokes what items' agents may do; a
// refused agent is told what it lacks and how to grant it.
func TestAccessFromTheCommandLine(t *testing.T) {
	f := newClientFixture(t)
	a, b := f.create(t, "Alpha"), f.create(t, "Beta")
	operator := f.cfg.API
	f.cfg.API, _ = f.api.AgentClients(t, a.Id)
	f.errs.Reset()
	if code := Run(t.Context(), []string{"show", "--item", b.Id}, f.cfg); code == ExitOK || !strings.Contains(f.errs.String(), "wi access grant --item "+a.Id+" --on "+b.Id+" --level view") {
		t.Fatalf("refusal: %d %s", code, f.errs)
	}
	f.cfg.API = operator
	if out := f.run(t, "access", "grant", "--item", a.Id, "--on", b.Id, "--level", "view"); !strings.Contains(out, "may view item "+b.Id) {
		t.Fatal(out)
	}
	f.cfg.API, _ = f.api.AgentClients(t, a.Id)
	f.run(t, "show", "--item", b.Id)
	f.cfg.API = operator
	if code := Run(t.Context(), []string{"access", "grant", "--item", a.Id, "--level", "view"}, f.cfg); code != ExitUsage {
		t.Fatalf("grant without --on: %d", code)
	}
	f.run(t, "access", "revoke", "item:"+a.Id+":"+b.Id)
	if strings.Contains(f.run(t, "access"), b.Id) {
		t.Fatal("revoked entry listed")
	}
}
