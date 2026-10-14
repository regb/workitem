package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/regb/workitem/internal/config"
)

func TestMissingDefaultProfileIsConfigurationError(t *testing.T) {
	cfg, warnings, err := config.Load(filepath.Join(t.TempDir(), "missing.toml"))
	if err == nil || !strings.Contains(err.Error(), "default_agent") || !strings.Contains(err.Error(), "wi init") || len(cfg.Agents) != 0 {
		t.Fatalf("cfg=%+v err=%v", cfg, err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %+v", warnings)
	}
	if cfg.DeepWork.MaxActive != 2 || cfg.List.RepositoryFolders != 2 || cfg.Attention.Priority != "recent-request" {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestExistingConfigWithoutDefaultProfileFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[agents.pi]\ndriver = \"pi\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := config.Load(path); err == nil || !strings.Contains(err.Error(), "default_agent") {
		t.Fatalf("expected missing default error: %v", err)
	}
}

func TestDefaultAgentReferenceValidation(t *testing.T) {
	for _, content := range []string{
		"default_agent = \"missing\"\n[agents.pi]\ndriver = \"pi\"\n",
		"default_agent = \"\"\n[agents.pi]\ndriver = \"pi\"\n",
		"default_agent = \"pi\"\n[agents.pi]\ndriver = \"pi\"\n[agents.default]\ndriver = \"pi\"\n",
	} {
		path := filepath.Join(t.TempDir(), "config.toml")
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := config.Load(path); err == nil {
			t.Fatalf("accepted %q", content)
		}
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

func TestLoadAgentDefinitionsAndDriverDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	writeFile(t, path, "[drivers.pi.defaults]\nexecutable = \"/opt/pi/bin/pi\"\n\n[agents.web-search]\ndriver = \"pi\"\nlabel = \"Pi · Web search\"\n\n[agents.web-search.config]\nextensions = [\"/opt/pi/web-search.ts\"]\nmodel = \"openai-codex/gpt-test\"\nthinking = \"high\"\n")
	cfg, warnings, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings=%v", warnings)
	}
	if cfg.DefaultAgent != "pi" || cfg.Agents["pi"].Driver != "pi" || cfg.Agents["web-search"].Driver != "pi" || cfg.Agents["web-search"].Label != "Pi · Web search" {
		t.Fatalf("agents=%+v", cfg.Agents)
	}
	if cfg.Drivers["pi"].Defaults["executable"] != "/opt/pi/bin/pi" {
		t.Fatalf("drivers=%+v", cfg.Drivers)
	}
	extensions, ok := cfg.Agents["web-search"].Config["extensions"].([]string)
	if !ok || len(extensions) != 1 || extensions[0] != "/opt/pi/web-search.ts" {
		t.Fatalf("extensions=%#v", cfg.Agents["web-search"].Config["extensions"])
	}
	if cfg.Agents["web-search"].Config["model"] != "openai-codex/gpt-test" || cfg.Agents["web-search"].Config["thinking"] != "high" {
		t.Fatalf("model config=%#v", cfg.Agents["web-search"].Config)
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
	if filepath.Base(path) == "config.toml" && !strings.Contains(content, "default_agent =") {
		content = "default_agent = \"pi\"\n[agents.pi]\ndriver = \"pi\"\n" + content
	}
	if err := osWriteFile(path, []byte(content)); err != nil {
		t.Fatal(err)
	}
}

var osWriteFile = func(path string, b []byte) error {
	return os.WriteFile(path, b, 0o600)
}
