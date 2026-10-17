package pi

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/regb/workitem/internal/testutil"
)

func TestTUIBridgeRecoversFromRelayRejectionAndRetainsOwnerFence(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node unavailable")
	}
	if err := exec.Command(node, "--experimental-strip-types", "--eval", "").Run(); err != nil {
		t.Skip("Node type stripping unavailable")
	}
	root := testutil.ShortTempDir(t)
	bridge := filepath.Join(root, "bridge.ts")
	if err := os.WriteFile(bridge, bridgeSource, 0600); err != nil {
		t.Fatal(err)
	}
	script := fmt.Sprintf(`import bridge from %q;
import assert from 'node:assert/strict';
import {createServer} from 'node:net';
const hooks=new Map(),accepted=[],attempts=new Map();let fence=false;let fenced=0;
const server=createServer(socket=>{let input='';socket.setEncoding('utf8');socket.on('data',chunk=>{input+=chunk;if(!input.includes('\n'))return;const event=JSON.parse(input.split('\n')[0]);attempts.set(event.event_id,(attempts.get(event.event_id)||0)+1);
if(fence){fenced++;socket.end(JSON.stringify({accepted:false,error:'runtime owner changed'})+'\n');return}
if(event.type==='agent.settled'&&attempts.get(event.event_id)<3){socket.end(JSON.stringify({accepted:false,retryable:true,error:'coordinator temporarily unavailable'})+'\n');return}
accepted.push(event);socket.end(JSON.stringify({accepted:true})+'\n')})});
await new Promise(resolve=>server.listen(process.env.WI_AGENT_EVENT_SOCKET,resolve));
bridge({on:(name,handler)=>hooks.set(name,handler),registerCommand:()=>{}});
const ctx={isIdle:()=>false,sessionManager:{getSessionId:()=> 'session',getSessionFile:()=>undefined}};
const fire=async(name,event={})=>await hooks.get(name)(event,ctx);
const until=async predicate=>{for(let i=0;i<300&&!predicate();i++)await new Promise(resolve=>setTimeout(resolve,10));assert.ok(predicate(),'runtime event delivery did not recover')};
await fire('session_start');await fire('agent_start');await fire('message_end',{message:{role:'assistant',content:[],stopReason:'stop'}});await fire('agent_before_settle',{outcome:'completed'});await fire('agent_settled');
await until(()=>accepted.some(x=>x.type==='agent.settled'));
const settled=accepted.find(x=>x.type==='agent.settled');assert.equal(settled.outcome,'completed');assert.equal(attempts.get(settled.event_id),3);assert.equal(accepted.filter(x=>x.type==='agent.settled').length,1);
fence=true;await fire('agent_start');await until(()=>fenced===1);await fire('turn_start');await fire('agent_settled');await new Promise(resolve=>setTimeout(resolve,200));assert.equal(fenced,1,'superseded owner kept sending');
await fire('session_shutdown');await new Promise(resolve=>server.close(resolve));
`, (&url.URL{Scheme: "file", Path: bridge}).String())
	file := filepath.Join(root, "test.mjs")
	if err := os.WriteFile(file, []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, "--experimental-strip-types", file)
	cmd.Env = append(os.Environ(), "WI_ID=item", "WI_AGENT_RUNTIME_ID=owner", "WI_AGENT_MODE=tui", "WI_AGENT_CONTROL_SOCKET="+filepath.Join(root, "control.sock"), "WI_AGENT_EVENT_SOCKET="+filepath.Join(root, "event.sock"))
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("TUI delivery recovery: %v\n%s", err, output)
	}
}
