package pi

import (
	"bufio"
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	driverprotocol "github.com/regb/workitem/agentdriver/protocol"
	"github.com/regb/workitem/internal/testutil"
)

// The TUI bridge streams content in TypeScript; Go subscribers apply its events
// with driverprotocol.ApplyContent and must reach the bridge's own snapshot.
func TestTUIBridgeStreamMatchesItsSnapshot(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node not installed")
	}
	if err := exec.Command(node, "--experimental-strip-types", "--eval", "").Run(); err != nil {
		t.Skip("Node type stripping unavailable")
	}
	root := testutil.ShortTempDir(t)
	bridge := filepath.Join(root, "bridge.ts")
	if err := os.WriteFile(bridge, bridgeSource, 0600); err != nil {
		t.Fatal(err)
	}
	// The script starts the bridge, then runs one step per line on stdin.
	script := fmt.Sprintf(`import bridge from %q;
import {createServer} from 'node:net';
import {createInterface} from 'node:readline';
const callbacks = new Map(); let leaf = 'leaf-1';
const relay=createServer(socket=>{let data='';socket.on('data',chunk=>{data+=chunk;if(data.includes('\n')) socket.end('{"accepted":true}\n');});});
await new Promise(resolve=>relay.listen(process.env.WI_AGENT_EVENT_SOCKET,resolve));
const ctx={isIdle:()=>true,hasPendingMessages:()=>false,shutdown:()=>{},sessionManager:{getBranch:()=>[{type:'message',id:'entry-9',message:{role:'assistant',timestamp:123}}],getLeafId:()=>leaf,buildSessionProjection:()=>({messages:[]}),getSessionId:()=> 'session',getSessionFile:()=> 'native'},ui:{notify:()=>{}}};
bridge({on:(name,fn)=>callbacks.set(name,fn),sendUserMessage:()=>{},events:{}});
await callbacks.get('session_start')({},ctx);
await new Promise(resolve=>setTimeout(resolve,60));
console.log('ready');
const steps={
  write:()=>{callbacks.get('message_start')({message:{role:'assistant',content:[]}},ctx);
    for (const delta of ['Hel','lo ','🌍']) callbacks.get('message_update')({assistantMessageEvent:{type:'text_delta',delta,partial:{}}},ctx);
    callbacks.get('message_update')({assistantMessageEvent:{type:'thinking_delta',delta:'pondering',partial:{}}},ctx);
    callbacks.get('tool_execution_start')({toolCallId:'t1',toolName:'bash',args:{command:'ls'}},ctx);
    callbacks.get('tool_execution_start')({toolCallId:'t2',toolName:'read',args:{path:'a'}},ctx);
    callbacks.get('tool_execution_end')({toolCallId:'t1',toolName:'bash',result:{content:[{type:'text',text:'a'}]},isError:false},ctx);},
  save:async()=>{callbacks.get('message_end')({message:{role:'assistant',content:[{type:'text',text:'Hello 🌍'}],stopReason:'stop',timestamp:123}},ctx);
    leaf='leaf-2';callbacks.get('session_tree')({oldLeafId:'leaf-1',newLeafId:leaf},ctx);
    callbacks.get('agent_settled')({},ctx);
    await new Promise(resolve=>setTimeout(resolve,20));},
  stop:async()=>{callbacks.get('session_shutdown')({},ctx);await new Promise(resolve=>setTimeout(resolve,60));relay.close();process.exit(0);},
};
for await (const line of createInterface({input:process.stdin})) { await steps[line](); console.log('done '+line); }
`, (&url.URL{Scheme: "file", Path: bridge}).String())
	scriptPath := filepath.Join(root, "stream.mjs")
	if err := os.WriteFile(scriptPath, []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	control := filepath.Join(root, "control.sock")
	cmd := exec.CommandContext(ctx, node, "--experimental-strip-types", scriptPath)
	cmd.Env = append(os.Environ(), "WI_ID=item-test", "WI_AGENT_RUNTIME_ID=tui-test", "WI_AGENT_MODE=tui", "WI_AGENT_CONTROL_SOCKET="+control, "WI_AGENT_EVENT_SOCKET="+filepath.Join(root, "events.sock"))
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	lines := bufio.NewScanner(stdout)
	expect := func(want string) {
		t.Helper()
		for lines.Scan() {
			if strings.TrimSpace(lines.Text()) == want {
				return
			}
		}
		t.Fatalf("bridge never printed %q", want)
	}
	expect("ready")
	path := driverprotocol.ContentSocketPath(control)
	values := make(chan driverprotocol.Content, 256)
	go func() {
		var value driverprotocol.Content
		_ = driverprotocol.SubscribeContent(ctx, path, "tui-test", "item-test", func(event driverprotocol.ContentEvent) error {
			value = driverprotocol.ApplyContent(value, event)
			values <- value
			return nil
		})
	}()
	// check runs one step and compares the streamed state with a fresh read.
	check := func(step string) driverprotocol.Content {
		t.Helper()
		fmt.Fprintln(stdin, step)
		expect("done " + step)
		want, err := driverprotocol.ReadContent(ctx, path, "tui-test", "item-test")
		if err != nil {
			t.Fatal(err)
		}
		deadline := time.After(3 * time.Second)
		for {
			select {
			case got := <-values:
				if got.Sequence == want.Sequence {
					if !reflect.DeepEqual(got, want) {
						t.Fatalf("after %s streamed %+v\nsnapshot %+v", step, got, want)
					}
					return got
				}
			case <-deadline:
				t.Fatalf("after %s the stream never reached sequence %d", step, want.Sequence)
			}
		}
	}
	got := check("write")
	if got.Text != "Hello 🌍" || got.Thought != "pondering" || got.Activity != "Tool completed: bash" || got.LeafID != "leaf-1" {
		t.Fatalf("while writing: %+v", got)
	}
	// Parallel calls are both kept, each with its own state.
	if len(got.Tools) != 2 || got.Tools[0].Status != "completed" || !strings.Contains(got.Tools[0].Output, `"text": "a"`) || got.Tools[1].Status != "running" {
		t.Fatalf("tools: %+v", got.Tools)
	}
	// The saved message keeps its text, now linked to its session entry.
	if got := check("save"); got.Text != "Hello 🌍" || got.Activity != "Idle" || got.LeafID != "leaf-2" || !reflect.DeepEqual(got.SavedAs, []string{"entry-9"}) {
		t.Fatalf("after saving: %+v", got)
	}
	fmt.Fprintln(stdin, "stop")
}
