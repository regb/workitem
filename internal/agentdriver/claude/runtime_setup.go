package claude

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	protocol "github.com/regb/workitem/agentdriver/protocol"
)

const workItemSystemPrompt = "This conversation belongs to a Wi work item. Read its task with `wi show` and inspect task context with `wi show --json`. File unrelated work as a separate item; do not change lifecycle, workspaces, or terminals without the user's authorization."

// runtimeSetup owns only resources shared by both modes. Native TUI hooks,
// terminal input, and headless JSON transport remain in their mode runners.
type runtimeSetup struct {
	executable, session, native, locator, skills string
	env                                          []string
	control                                      *protocol.ControlServer
	content                                      *protocol.ContentServer
	cleanupSkills                                func()
	config                                       Config
}

func prepareRuntime(r protocol.LaunchRequest, c Config) (*runtimeSetup, error) {
	executable, err := exec.LookPath(c.Executable)
	if err != nil {
		return nil, err
	}
	if !filepath.IsAbs(r.Runtime.CWD) || !filepath.IsAbs(r.Conversation.Locator) {
		return nil, fmt.Errorf("Claude requires absolute workspace and conversation paths")
	}
	session, err := sessionID(r.Conversation.Locator)
	if err != nil {
		return nil, err
	}
	env, native, err := prepareStorage(r.Conversation.Locator, session, r.Environment)
	if err != nil {
		return nil, err
	}
	p := &runtimeSetup{executable: executable, session: session, native: native, locator: r.Conversation.Locator, env: runtimeEnvironment(env, r.Runtime.Mode), config: c}
	success := false
	defer func() {
		if !success {
			p.close()
		}
	}()
	p.control, err = protocol.ListenControl(r.Runtime.ControlSocketPath)
	if err != nil {
		return nil, err
	}
	p.content, err = protocol.ListenContent(protocol.ContentSocketPath(r.Runtime.ControlSocketPath), r.Runtime.ID, r.Runtime.WorkItemID, false)
	if err != nil {
		return nil, err
	}
	p.skills, p.cleanupSkills, err = exportSkills(r.Skills)
	if err != nil {
		return nil, err
	}
	success = true
	return p, nil
}

func runtimeEnvironment(env []string, mode protocol.Mode) []string {
	if mode != protocol.ModeHeadless {
		return env
	}
	filtered := make([]string, 0, len(env))
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		if key != "TMUX" && key != "TMUX_PANE" && key != "TMUX_TMPDIR" {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}

func (p *runtimeSetup) close() {
	if p.cleanupSkills != nil {
		p.cleanupSkills()
	}
	if p.content != nil {
		_ = p.content.Close()
	}
	if p.control != nil {
		_ = p.control.Close()
	}
}

func (p *runtimeSetup) args(modeArgs ...string) ([]string, error) {
	sessionArgs, err := conversationLaunchArgs(p.locator, p.session)
	if err != nil {
		return nil, err
	}
	// Both modes bypass per-tool permission prompts on creation and resume.
	// This is not a sandbox or permission to answer authentication/trust dialogs.
	args := []string{"--dangerously-skip-permissions", "--append-system-prompt", workItemSystemPrompt}
	args = append(args, modeArgs...)
	args = append(args, sessionArgs...)
	if p.skills != "" {
		args = append(args, "--plugin-dir", p.skills)
	}
	if p.config.Model != "" {
		args = append(args, "--model", p.config.Model)
	}
	if p.config.Effort != "" {
		args = append(args, "--effort", p.config.Effort)
	}
	return args, nil
}

func conversationLaunchArgs(locator, session string) ([]string, error) {
	info, err := os.Lstat(locator)
	if err == nil {
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("Claude transcript must be a regular file")
		}
		if info.Size() > 0 {
			return []string{"--resume", session}, nil
		}
		// Core creates an empty regular placeholder. Claude rejects --session-id
		// while that file exists. Never remove nonempty/native transcript content.
		if err := os.Remove(locator); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	return []string{"--session-id", session}, nil
}
