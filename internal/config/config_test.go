package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/regb/workitem/internal/config"
)

func TestLoadDefaultsWhenMissing(t *testing.T) {
	cfg, warnings, err := config.Load(filepath.Join(t.TempDir(), "missing.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %+v", warnings)
	}
	if cfg.DeepWork.MaxActive != 2 || cfg.List.RepositoryFolders != 2 || cfg.Attention.Priority != "recent-request" {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestLoadDirenvAutoTrustRepositories(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	writeFile(t, path, "[direnv]\nauto_trust_repositories = [\"/srv/repos/one\", \"/srv/repos/two\"]\n")
	cfg, warnings, err := config.Load(path)
	if err != nil || len(warnings) != 0 || len(cfg.Direnv.AutoTrustRepositories) != 2 {
		t.Fatalf("cfg=%+v warnings=%v err=%v", cfg, warnings, err)
	}
}

func TestLoadRejectsRelativeDirenvAutoTrustRepository(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	writeFile(t, path, "[direnv]\nauto_trust_repositories = [\"relative/repo\"]\n")
	if _, _, err := config.Load(path); err == nil {
		t.Fatal("expected relative auto-trust path rejection")
	}
}

func TestLoadDeepWorkListAndAgentStatusConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	writeFile(t, path, "[deep_work]\nmax_active = 3\n\n[item.defaults]\nlabels = [\"personal\", \"Go Work\"]\n\n[list]\nrepository_folders = 3\nlabels = [\"+networking\", \"-personal\",]\n\n[attention]\npriority = \"recent-request\"\n\n[agent_status.markers]\nbusy = \"🤖\"\nidle = \"💬\"\nproblem = \"⚠️\"\n")
	cfg, _, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DeepWork.MaxActive != 3 || len(cfg.Item.Defaults.Labels) != 2 || cfg.Item.Defaults.Labels[1] != "Go Work" || cfg.List.RepositoryFolders != 3 || len(cfg.List.Labels) != 2 || cfg.List.Labels[0] != "+networking" || cfg.List.Labels[1] != "-personal" || cfg.Attention.Priority != "recent-request" || cfg.AgentStatus.Markers.Busy != "🤖" || cfg.AgentStatus.Markers.Idle != "💬" || cfg.AgentStatus.Markers.Problem != "⚠️" {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestLoadProjectItemDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wi.toml")
	writeFile(t, path, "[item.defaults]\nlabels = [\"backend\", \"team-one\"]\n\n[hooks.post-start]\nserver = \"ignored\"\n")
	cfg, warnings, err := config.LoadProject(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 || len(cfg.Item.Defaults.Labels) != 2 || cfg.Item.Defaults.Labels[0] != "backend" {
		t.Fatalf("cfg=%+v warnings=%+v", cfg, warnings)
	}
}

func TestRejectInvalidProjectDefaultLabel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wi.toml")
	writeFile(t, path, "[item.defaults]\nlabels = [\"bad!label\"]\n")
	if _, _, err := config.LoadProject(path); err == nil {
		t.Fatal("expected invalid project default label to fail")
	}
}

func TestRejectInvalidListLabelsArray(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	writeFile(t, path, "[list]\nlabels = \"+backend\"\n")
	if _, _, err := config.Load(path); err == nil {
		t.Fatal("expected non-array labels to fail")
	}
}

func TestRejectUnknownAttentionPriority(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	writeFile(t, path, "[attention]\npriority = \"missing\"\n")
	if _, _, err := config.Load(path); err == nil {
		t.Fatal("expected unknown priority strategy to fail")
	}
}

func TestRejectNegativeLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	writeFile(t, path, "[deep_work]\nmax_active = -1\n")
	if _, _, err := config.Load(path); err == nil {
		t.Fatal("expected negative limit to fail")
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := osWriteFile(path, []byte(content)); err != nil {
		t.Fatal(err)
	}
}

var osWriteFile = func(path string, b []byte) error {
	return os.WriteFile(path, b, 0o600)
}
