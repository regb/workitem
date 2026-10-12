package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRetiredBoxConfigIsNotAliased(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[boxes.dev]\ndriver=\"docker\"\nimage=\"dev\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, warnings, err := Load(path)
	if err != nil || len(cfg.Executors) != 0 || len(warnings) == 0 {
		t.Fatalf("old config aliased: %+v %v %v", cfg.Executors, warnings, err)
	}
}

func TestExecutorProfiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[executors.dev]\ndriver = \"docker\"\nimage = \"wi-executor-test\"\nnetwork = \"none\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, warnings, err := Load(path)
	if err != nil || len(warnings) != 0 {
		t.Fatalf("%v %v", warnings, err)
	}
	if cfg.Executors["dev"].Image != "wi-executor-test" {
		t.Fatalf("%+v", cfg.Executors)
	}
	if err := os.WriteFile(path, []byte("[executors.remote]\ndriver=\"ssh\"\nhost=\"builder\"\nroot=\"/home/dev/wi\"\nssh_config=\"/home/dev/.ssh/config\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	remote, _, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if remote.Executors["remote"].Host != "builder" || remote.Executors["remote"].Root != "/home/dev/wi" {
		t.Fatalf("%+v", remote.Executors)
	}
	for _, content := range []string{
		"[executors.dev]\ndriver=\"ssh\"\nimage=\"dev\"\n",
		"[executors.dev]\ndriver=\"docker\"\n",
		"[executors.dev]\ndriver=\"docker\"\nimage=\"dev\"\nnetwork=\"host\"\n",
		"[executors.dev]\ndriver=\"docker\"\nimage=\"dev\"\nprivileged=true\n",
	} {
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := Load(path); err == nil {
			t.Fatalf("accepted %q", content)
		}
	}
}
