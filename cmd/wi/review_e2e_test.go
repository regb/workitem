package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
)

func (f *binaryFixture) reviewWeb(item, url string, curl func(string, any) []byte) {
	f.t.Helper()
	ctx := context.Background()
	workspace, err := f.clients.Workspaces.GetWorkspace(ctx, connect.NewRequest(&v1.GetWorkspaceRequest{ItemId: item}))
	if err != nil {
		f.t.Fatal(err)
	}
	claim := workspace.Msg.Checkout.Claim
	if err := os.WriteFile(filepath.Join(claim.Path, "review.txt"), []byte("review before\n"), 0600); err != nil {
		f.t.Fatal(err)
	}
	prefix := "/api/items/" + item
	var files v1.ListWorkspaceFilesResponse
	if err := json.Unmarshal(curl(prefix+"/workspace-files", nil), &files); err != nil || len(files.Paths) != 1 {
		f.t.Fatalf("file list: %v %v", files.String(), err)
	}
	query := fmt.Sprintf("?path=review.txt&claim_id=%s&claim_generation=%d", claim.Id, claim.Generation)
	var source v1.GetWorkspaceFileResponse
	if err := json.Unmarshal(curl(prefix+"/source"+query, nil), &source); err != nil || source.File.Content != "review before\n" {
		f.t.Fatalf("source: %v %v", source.String(), err)
	}
	curl(prefix+"/source", map[string]any{"path": "review.txt", "content": "review after\n", "expected_sha256": source.File.Sha256, "claim_id": claim.Id, "claim_generation": claim.Generation})
	var review v1.GetReviewResponse
	if err := json.Unmarshal(curl(prefix+"/review", nil), &review); err != nil || len(review.Files) != 1 {
		f.t.Fatalf("review: %v %v", review.String(), err)
	}
	var diff v1.GetReviewFileResponse
	if err := json.Unmarshal(curl(prefix+"/review-file?path=review.txt&expected_version="+review.Revision.Version, nil), &diff); err != nil || diff.NewFile.Content != "review after\n" {
		f.t.Fatalf("diff: %v %v", diff.String(), err)
	}
	var checkpoint struct {
		Result v1.CaptureReviewRevisionResponse `json:"result"`
	}
	if err := json.Unmarshal(curl(prefix+"/checkpoints", map[string]any{"capture_id": "web-checkpoint", "claim_id": claim.Id, "claim_generation": claim.Generation}), &checkpoint); err != nil || checkpoint.Result.Revision == nil || checkpoint.Result.Revision.ObservedBranch != workspace.Msg.Checkout.Branch {
		f.t.Fatalf("web checkpoint: %v %v", checkpoint.Result.String(), err)
	}
	var retained v1.ListReviewRevisionsResponse
	if err := json.Unmarshal(curl(prefix+"/review-revisions", nil), &retained); err != nil || len(retained.Revisions) == 0 {
		f.t.Fatalf("web revisions: %v %v", retained.String(), err)
	}
	comparison := "?revision_id=" + checkpoint.Result.Revision.Id
	var immutable v1.GetReviewResponse
	if err := json.Unmarshal(curl(prefix+"/review"+comparison, nil), &immutable); err != nil || immutable.Revision.WorkingTree {
		f.t.Fatalf("immutable comparison: %v %v", immutable.String(), err)
	}
	var snapshotFile v1.GetReviewFileResponse
	if err := json.Unmarshal(curl(prefix+"/review-file"+comparison+"&path=review.txt&expected_version="+immutable.Revision.Version, nil), &snapshotFile); err != nil || snapshotFile.NewFile.Content != "review after\n" {
		f.t.Fatalf("snapshot source: %v %v", snapshotFile.String(), err)
	}
	if os.Getenv("REVIEW_BROWSER_SMOKE") != "" {
		var linked strings.Builder
		for line := 1; line <= 400; line++ {
			fmt.Fprintf(&linked, "// linked line %d\n", line)
		}
		if err := os.WriteFile(filepath.Join(claim.Path, "z-linked.ts"), []byte(linked.String()), 0600); err != nil {
			f.t.Fatal(err)
		}
		text := strings.Repeat("Context above the reference is long enough to wrap differently when opening the code panel beside the conversation.\n\n", 12) + "Check `z-linked.ts:240:3`\n\n```markdown\n# Clipboard smoke\n\nA **markdown** block.\n```" + strings.Repeat("\n\nMore conversation below the reference keeps it away from the bottom while the reader inspects this particular file.", 12)
		curl(prefix+"/messages", map[string]string{"text": text, "delivery": "prompt", "client_id": "file-link-smoke"})
		f.historyReady(item, 4)
		f.idle(item)
	}
	if os.Getenv("REVIEW_BROWSER_SMOKE") != "" {
		restore := f.seedReviewGraph(claim.Path)
		f.reviewBrowser(item, url, claim.Path, checkpoint.Result.Revision.Id)
		restore()
	}
	if _, err := f.clients.Agents.StopAgent(ctx, connect.NewRequest(&v1.StopAgentRequest{ItemId: item, ConfirmAttachedTerminal: true})); err != nil {
		f.t.Fatal(err)
	}
	curl(prefix+"/commit", map[string]string{"message": "Reviewed source"})
	var history v1.ListReviewCommitsResponse
	if err := json.Unmarshal(curl(prefix+"/commits", nil), &history); err != nil || len(history.Commits) < 2 || len(history.Branches) < 2 {
		f.t.Fatalf("commit history: %v %v", history.String(), err)
	}
}

// Optional real-Chrome smoke test of the embedded production build, its CSP,
// Pierre's shadow DOM/editor and worker assets. Normal tests need no browser.
// REVIEW_BROWSER_SMOKE=1 TMPDIR=/tmp go test ./cmd/wi -run '^TestCLIAPIEndToEnd$' -count=1 -v
func (f *binaryFixture) reviewBrowser(item, url, checkout, revisionID string) {
	if os.Getenv("REVIEW_BROWSER_SMOKE") == "" {
		return
	}
	browser, err := exec.LookPath("agent-browser")
	if err != nil {
		f.t.Fatal(err)
	}
	session := "wi-review-" + filepath.Base(f.root)
	run := func(args ...string) string {
		f.t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
		defer cancel()
		flags := []string{"--session", session}
		chrome := "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
		if _, err := os.Stat(chrome); err == nil {
			flags = append(flags, "--executable-path", chrome)
		}
		cmd := exec.CommandContext(ctx, browser, append(flags, args...)...)
		cmd.Env = f.env
		out, err := cmd.CombinedOutput()
		if err != nil {
			diagnostic := exec.CommandContext(ctx, browser, append(flags, "eval", "JSON.stringify({body:document.body.innerText.slice(-2200),files:[...document.querySelectorAll('diffs-container')].map(file=>({dataset:{...file.dataset},text:file.shadowRoot?.textContent.slice(-1800),ancestors:(()=>{let n=file;const out=[];while(n){out.push({tag:n.tagName,top:n.scrollTop,height:n.scrollHeight,client:n.clientHeight,overflow:getComputedStyle(n).overflowY});n=n.parentElement}return out})()}))})")...)
			diagnostic.Env = f.env
			if details, _ := diagnostic.CombinedOutput(); len(details) > 0 {
				f.t.Logf("browser source diagnostics: %s", details)
			}
			f.t.Fatalf("browser %v: %v %s", args, err, out)
		}
		return string(out)
	}
	defer func() { cmd := exec.Command(browser, "--session", session, "close"); cmd.Env = f.env; _ = cmd.Run() }()
	// pick chooses an option of one of the Change header's pickers. It opens
	// the list and clicks the option in one script: the list closes when the
	// window loses focus, which separate browser commands can cause.
	pick := func(label, value string) {
		f.t.Helper()
		script := fmt.Sprintf("(async()=>{const trigger=document.querySelector('[aria-label=%q]');trigger.focus();trigger.dispatchEvent(new KeyboardEvent('keydown',{key:'Enter',bubbles:true}));for(let i=0;i<60;i++){const option=document.querySelector('[role=option][data-value=%q]');if(option){option.click();return true}await new Promise(resolve=>setTimeout(resolve,50))}return false})()", label, value)
		if result := run("eval", script); !strings.Contains(result, "true") {
			f.t.Fatalf("no %s option %s: %s", label, value, result)
		}
		run("wait", "--fn", fmt.Sprintf("document.querySelector('[aria-label=%q]').dataset.value===%q", label, value))
		// While the list closes, the page ignores pointer input.
		run("wait", "--fn", "!document.querySelector('[role=listbox]') && document.body.style.pointerEvents!=='none'")
	}
	// On the dashboard, one click outside closes the agent chooser and the
	// create box's backdrop together.
	run("open", url+"/")
	run("set", "viewport", "1280", "800")
	run("wait", "--fn", "(()=>{const b=document.querySelector('[aria-label^=\"Agent:\"]');return !!b&&!b.disabled})()")
	run("click", "[aria-label='New task']")
	run("click", "[aria-label^='Agent:']")
	run("wait", "[aria-label='Agent profile']")
	run("wait", ".backdrop-blur-sm")
	run("mouse", "move", "200", "150")
	run("mouse", "down")
	run("mouse", "up")
	run("wait", "--fn", "!document.querySelector('[aria-label=\"Agent profile\"]') && !document.querySelector('.backdrop-blur-sm')")
	run("open", url+"/items/"+item)
	run("wait", "--fn", "[...document.querySelectorAll('button')].some(b=>b.textContent.trim()==='Code')")
	run("find", "role", "button", "click", "--name", "Code", "--exact")
	run("wait", "[aria-label='Code review']")
	run("wait", "diffs-container")
	run("wait", "--fn", "document.querySelector('diffs-container')?.shadowRoot?.textContent.includes('review after')")
	// Change layout: a one-line header, then the full-width changed-file list,
	// then the diff. No commit form: the message is asked for when merging.
	run("wait", "[aria-label='Changed files']")
	if result := run("eval", "(()=>{const box=label=>document.querySelector(`[aria-label=\"${label}\"]`).getBoundingClientRect(),header=box('Change header'),files=box('Changed files'),diff=box('File diff'),panel=box('Code review');return !document.querySelector('[aria-label=\"Change review\"] textarea, [aria-label=\"Change header\"] input')&&header.bottom<=files.top&&files.bottom<=diff.top&&diff.width>panel.width*.95&&Math.abs(files.width-diff.width)<2})()"); !strings.Contains(result, "true") {
		f.t.Fatalf("unexpected Change layout: %s", result)
	}
	run("screenshot", filepath.Join(os.TempDir(), "wi-change-layout.png"))
	run("set", "viewport", "390", "844")
	run("wait", "--fn", "document.documentElement.scrollWidth<=innerWidth && document.querySelector('[aria-label=\"Changed files\"]').getBoundingClientRect().width<=innerWidth")
	run("screenshot", filepath.Join(os.TempDir(), "wi-change-mobile.png"))
	run("set", "viewport", "1280", "800")
	// Checkpoints use private retained commits, not the mutable Git view.
	pick("Revision", "rev:"+revisionID)
	run("wait", "--text", revisionID+" is a saved snapshot")
	run("wait", "--fn", "document.querySelector('diffs-container')?.shadowRoot?.textContent.includes('review after')")
	if result := run("eval", "![...document.querySelectorAll('button')].some(b=>b.textContent.includes('Merge…'))"); !strings.Contains(result, "true") {
		f.t.Fatalf("a snapshot offered to merge: %s", result)
	}
	run("find", "role", "button", "click", "--name", "Capture checkpoint", "--exact")
	run("wait", "--fn", fmt.Sprintf("document.querySelector('[aria-label=\"Revision\"]').dataset.value!==%q", "rev:"+revisionID))
	pick("Compared to", "rev:"+revisionID)
	run("wait", "--fn", "document.querySelector('diffs-container')?.shadowRoot?.textContent.includes('linked line 1')")
	run("screenshot", filepath.Join(os.TempDir(), "wi-checkpoints.png"))
	run("select", "[aria-label='Comment file']", "")
	run("find", "label", "Review comment", "fill", "Browser feedback stays anchored to this revision")
	run("find", "role", "button", "click", "--name", "Save comment draft", "--exact")
	run("wait", "--text", "Saved d1")
	run("wait", "--fn", "[...document.querySelectorAll('button')].some(b=>b.textContent.trim()==='Publish feedback (1)'&&!b.disabled)")
	run("eval", "[...document.querySelectorAll('button')].find(b=>b.textContent.trim()==='Publish feedback (1)').scrollIntoView({block:'center'})")
	run("find", "role", "button", "click", "--name", "Publish feedback (1)", "--exact")
	// Publication survives a notification refusal (for example another
	// controller's lease). Do not take over that controller to make review pass.
	run("wait", "[aria-label='Thread t1']")
	run("find", "text", "Published rounds (1)", "click", "--exact")
	run("wait", "[aria-label='Published round q1']")
	run("find", "role", "button", "click", "--name", "Notify agent for q1", "--exact")
	run("wait", "--fn", "(()=>{const text=document.querySelector('[aria-label=\"Published round q1\"]')?.textContent??'';return text.includes('Notification sent for q1.')||text.includes('q1 remains published.')})()")
	run("screenshot", filepath.Join(os.TempDir(), "wi-review-feedback.png"))
	pick("Revision", "tip")
	run("wait", "--text", "No changes between these revisions.")
	pick("Revision", "working")
	run("wait", "--fn", "document.querySelector('diffs-container')?.shadowRoot?.textContent.includes('review after')")
	// Contextual history shows only the selected branch tips and shared ancestry.
	run("find", "role", "button", "click", "--name", "History", "--exact")
	run("wait", "[aria-label='Commit graph']")
	run("wait", "--fn", "document.querySelectorAll('[data-commit-sha]').length===3")
	if result := run("eval", "!document.querySelector('[aria-label=\"Commit graph\"]').textContent.includes('Unrelated secret branch') && document.querySelector('[aria-label=\"Commit graph\"]').textContent.includes('review-origin/main')"); !strings.Contains(result, "true") {
		f.t.Fatalf("unexpected history scope: %s", result)
	}
	run("screenshot", filepath.Join(os.TempDir(), "wi-commit-graph.png"))
	run("find", "text", "Remote graph context", "click", "--exact")
	run("wait", "--fn", "/^commit:[0-9a-f]{40}$/.test(document.querySelector('[aria-label=\"Revision\"]').dataset.value)")
	pick("Revision", "working")
	run("wait", "--fn", "document.querySelector('diffs-container')?.shadowRoot?.textContent.includes('review after')")
	// Code is the primary desktop workspace, with chat kept sidebar-sized.
	run("set", "viewport", "1280", "800")
	run("wait", "--fn", "document.querySelector('[aria-label=\"Agent chat\"]').getBoundingClientRect().width===600")
	if result := run("eval", "document.querySelector('[aria-label=\"Code review\"]').getBoundingClientRect().width > document.querySelector('[aria-label=\"Agent chat\"]').getBoundingClientRect().width"); !strings.Contains(result, "true") {
		f.t.Fatalf("code is not the primary workspace: %s", result)
	}
	run("focus", "[aria-label='Resize chat sidebar']")
	run("press", "End")
	run("wait", "--fn", "document.querySelector('[aria-label=\"Agent chat\"]').getBoundingClientRect().width===720")
	run("set", "viewport", "390", "844")
	run("wait", "--fn", "document.querySelector('[aria-label=\"Code review\"]').getBoundingClientRect().width===innerWidth && document.querySelector('[aria-label=\"Agent chat\"]').getBoundingClientRect().width===0")
	run("screenshot", filepath.Join(os.TempDir(), "wi-code-sidebar-mobile.png"))
	run("find", "role", "button", "click", "--name", "Back to chat", "--exact")
	run("wait", "--fn", "!document.querySelector('[aria-label=\"Code review\"]') && document.querySelector('[aria-label=\"Agent chat\"]').getBoundingClientRect().width===innerWidth")
	run("find", "role", "button", "click", "--name", "Code", "--exact")
	run("wait", "[aria-label='Code review']")
	run("set", "viewport", "1280", "800")
	run("focus", "[aria-label='Resize chat sidebar']")
	run("press", "Home")
	for range 5 {
		run("press", "ArrowRight")
	}
	run("wait", "--fn", "document.querySelector('[aria-label=\"Agent chat\"]').getBoundingClientRect().width===400")
	run("screenshot", filepath.Join(os.TempDir(), "wi-code-sidebar-desktop.png"))
	// Message links must reveal virtualized lines, including a repeated click
	// on the same file after the reader has scrolled elsewhere.
	chip := "[data-role='assistant'] [data-file-link='z-linked.ts']"
	run("wait", chip)
	run("find", "role", "button", "click", "--name", "Close code panel", "--exact")
	run("wait", "--fn", "!document.querySelector('[aria-label=\"Code review\"]') && document.querySelector('[aria-label=\"Agent chat\"]').getBoundingClientRect().width===innerWidth")
	run("focus", chip)
	run("press", "ArrowLeft")
	run("eval", "document.querySelector(\"[data-role='assistant'] [data-file-link='z-linked.ts']\").addEventListener('pointerdown',event=>{window.fileChipOffset=event.currentTarget.getBoundingClientRect().top-document.querySelector('[aria-label=\"Conversation history\"]').getBoundingClientRect().top},{once:true})")
	revealed := "(()=>{const file=document.querySelector('diffs-container');let node=file?.parentElement;while(node&&getComputedStyle(node).overflowY!=='auto')node=node.parentElement;return !!node&&node.scrollTop>1000&&file.shadowRoot.textContent.includes('linked line 240')})()"
	for attempt := range 2 {
		run("click", chip)
		run("wait", "--text", "z-linked.ts:240:3")
		run("wait", "--fn", revealed)
		run("wait", "--fn", "Math.abs(document.querySelector(\"[data-role='assistant'] [data-file-link='z-linked.ts']\").getBoundingClientRect().top-document.querySelector('[aria-label=\"Conversation history\"]').getBoundingClientRect().top-window.fileChipOffset)<2")
		if attempt == 0 {
			run("eval", "(()=>{let node=document.querySelector('diffs-container').parentElement;while(node&&getComputedStyle(node).overflowY!=='auto')node=node.parentElement;node.scrollTop=0})()")
		}
	}
	run("set", "viewport", "390", "844")
	run("find", "role", "button", "click", "--name", "Back to chat", "--exact")
	run("find", "role", "button", "click", "--name", "Take control", "--exact")
	run("wait", "--fn", "(()=>{const button=[...document.querySelectorAll('[role=alertdialog] button')].find(b=>b.textContent.trim()==='Take control');if(!button)return false;const r=button.getBoundingClientRect();return button.contains(document.elementFromPoint(r.x+r.width/2,r.y+r.height/2))})()")
	run("click", "[role=alertdialog] [data-slot=alert-dialog-action]")
	run("wait", "--fn", "!document.querySelector('[data-slot=alert-dialog-overlay]')")
	run("wait", "textarea[aria-label='Message']")
	run("find", "role", "textbox", "fill", "--name", "Message", "--exact", "Keep my mobile draft")
	run("click", chip)
	run("wait", "--fn", revealed)
	run("wait", "--fn", "document.querySelector('[aria-label=\"Code review\"]').getBoundingClientRect().width===innerWidth")
	run("find", "role", "button", "click", "--name", "Back to chat", "--exact")
	run("wait", "--fn", "document.querySelector('[aria-label=\"Message\"]').value==='Keep my mobile draft'")
	// Simulate an HTTP origin without the secure Clipboard API. The browser's
	// actual clipboard must receive the raw fenced Markdown, not its HTML.
	run("eval", "Object.defineProperty(navigator,'clipboard',{configurable:true,value:undefined});document.addEventListener('copy',()=>{window.fallbackCopyUsed=true},{once:true})")
	copyButton := "[data-role='assistant'] [data-slot=code-block-copy-button]"
	run("hover", "[data-role='assistant'] [data-slot=code-block]")
	run("click", copyButton)
	run("wait", "[data-role='assistant'] [data-slot=code-block-copy-button][data-copied=true]")
	run("wait", "--fn", "window.fallbackCopyUsed===true")
	run("wait", "--fn", "document.querySelector('[aria-label=\"Message\"]').value==='Keep my mobile draft'")
	// Restore API visibility only to inspect what the fallback actually put
	// on Chrome's clipboard. The copy gesture above ran without that API.
	run("eval", "delete navigator.clipboard")
	copied := f.readBrowserClipboard(run("get", "cdp-url"), url)
	if strings.TrimSpace(copied) != "# Clipboard smoke\n\nA **markdown** block." {
		f.t.Fatal("browser clipboard did not contain the raw Markdown block")
	}
	run("find", "role", "button", "click", "--name", "Code", "--exact")
	run("set", "viewport", "1280", "800")
	run("wait", "--fn", revealed)
	run("screenshot", filepath.Join(os.TempDir(), "wi-chat-file-link.png"))
	run("find", "role", "button", "click", "--name", "Change", "--exact")
	run("wait", "--fn", "document.querySelector('diffs-container')?.shadowRoot?.textContent.includes('review after')")
	if errors := run("errors"); strings.Contains(errors, "Error") || strings.Contains(errors, "error:") {
		f.t.Fatalf("file-link browser errors: %s", errors)
	}
	if os.Getenv("REVIEW_BROWSER_LAYOUT_ONLY") != "" {
		return
	}
	// Keep the action away from the transient clipboard toast at the bottom.
	run("eval", "[...document.querySelectorAll('button')].find(b=>b.textContent.trim()==='Split').scrollIntoView({block:'center'})")
	run("find", "role", "button", "click", "--name", "Split", "--exact")
	run("find", "role", "button", "click", "--name", "Open file", "--exact")
	run("wait", "--fn", "[...document.querySelectorAll('button')].some(b=>b.textContent.trim()==='Edit'&&!b.disabled)")
	run("find", "role", "button", "click", "--name", "Edit", "--exact")
	ready := "document.querySelector('diffs-container')?.shadowRoot?.querySelector('[contenteditable=true]')"
	typeEdit := func() {
		run("wait", "--fn", ready)
		run("find", "role", "textbox", "click", "--name", "review.txt", "--exact")
		// Individual keys exercise Pierre's native keyboard input in its shadow DOM.
		run("press", "ControlOrMeta+End")
		for _, key := range []string{"b", "r", "o", "w", "s", "e", "r", "Space", "e", "d", "i", "t"} {
			run("press", key)
		}
	}
	typeEdit()
	run("wait", "--fn", "document.querySelector('diffs-container')?.shadowRoot?.textContent.includes('browser edit')")
	// Closing the panel must not discard the active editor's draft or selection.
	run("find", "role", "button", "click", "--name", "Close code panel", "--exact")
	run("find", "role", "button", "click", "--name", "Code", "--exact")
	run("wait", "--fn", ready)
	run("wait", "--fn", "document.querySelector('diffs-container')?.shadowRoot?.textContent.includes('browser edit')")
	// A competing filesystem edit must fail the save without consuming the draft.
	if err := os.WriteFile(filepath.Join(checkout, "review.txt"), []byte("agent changed\n"), 0600); err != nil {
		f.t.Fatal(err)
	}
	run("find", "role", "button", "click", "--name", "Save", "--exact")
	run("wait", "--text", "Your draft has been kept.")
	run("wait", "--fn", "document.querySelector('diffs-container')?.shadowRoot?.textContent.includes('browser edit')")
	run("find", "role", "button", "click", "--name", "Discard", "--exact")
	run("wait", "--fn", "(()=>{const button=[...document.querySelectorAll('button')].find(b=>b.textContent.trim()==='Discard and reload');if(!button)return false;const r=button.getBoundingClientRect();return button.contains(document.elementFromPoint(r.x+r.width/2,r.y+r.height/2))})()")
	run("find", "role", "button", "click", "--name", "Discard and reload", "--exact")
	run("wait", "--fn", "!document.querySelector('[role=alertdialog]')")
	run("wait", "--fn", "[...document.querySelectorAll('button')].some(b=>b.textContent.trim()==='Edit'&&!b.disabled)")
	run("find", "role", "button", "click", "--name", "Edit", "--exact")
	typeEdit()
	run("find", "role", "button", "click", "--name", "Save", "--exact")
	run("wait", "--text", "Edit")
	data, err := os.ReadFile(filepath.Join(checkout, "review.txt"))
	if err != nil || !strings.Contains(string(data), "browser edit") {
		f.t.Fatalf("browser save: %q %v; %s", data, err, run("snapshot", "-i", "-c"))
	}
	if errors := run("errors"); strings.Contains(errors, "Error") || strings.Contains(errors, "error:") {
		f.t.Fatalf("browser errors: %s", errors)
	}
	run("set", "viewport", "390", "844")
	run("wait", "--fn", "document.querySelector('[aria-label=\"Code review\"]').getBoundingClientRect().width <= innerWidth")
	if result := run("eval", "document.documentElement.scrollWidth <= innerWidth"); !strings.Contains(result, "true") {
		f.t.Fatalf("mobile overflow: %s", result)
	}
	run("screenshot", filepath.Join(os.TempDir(), "wi-review-browser.png"))
}
