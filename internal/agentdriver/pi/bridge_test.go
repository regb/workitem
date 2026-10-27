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

func TestBridgeObservesSelectedBranchAndNativeLifecycle(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node unavailable")
	}
	if err := exec.Command(node, "--experimental-strip-types", "--eval", "").Run(); err != nil {
		t.Skip("Node type stripping unavailable")
	}
	for _, mode := range []string{"tui", "headless"} {
		t.Run(mode, func(t *testing.T) {
			root := testutil.ShortTempDir(t)
			bridge := filepath.Join(root, "bridge.ts")
			if err := os.WriteFile(bridge, bridgeSource, 0600); err != nil {
				t.Fatal(err)
			}
			script := fmt.Sprintf(`import bridge from %q;
import assert from 'node:assert/strict';
import fs from 'node:fs';
import {syncBuiltinESMExports} from 'node:module';
import {createServer,connect} from 'node:net';
const statuses=[],events=[],hooks=new Map();let tick;let timerCleared=0;
globalThis.setInterval=fn=>{tick=fn;return {unref(){}}};globalThis.clearInterval=()=>{timerCleared++;tick=undefined};
let server;
if(process.env.WI_AGENT_MODE==='tui'){
 server=createServer(socket=>{let input='';socket.setEncoding('utf8');socket.on('data',chunk=>{input+=chunk;if(!input.includes('\n'))return;const event=JSON.parse(input.split('\n')[0]);events.push(event);if(event.type==='runtime.status')statuses.push(event);socket.end(JSON.stringify({accepted:true})+'\n')})});
 await new Promise(resolve=>server.listen(process.env.WI_AGENT_EVENT_SOCKET,resolve));
}else{
 fs.writeSync=(fd,raw)=>{assert.equal(fd,1);assert.ok(raw.endsWith('\n'),'private status was not JSONL');const event=JSON.parse(raw);events.push(event);if(event.type==='wi_runtime_status')statuses.push(event);return raw.length};syncBuiltinESMExports();
}
let idle=true,pending=false,leaf='good';let messages=[{role:'assistant',stopReason:'stop',content:'native-private-good'}];let scans=0;
const ctx={isIdle:()=>idle,hasPendingMessages:()=>pending,sessionManager:{getLeafId:()=>leaf,getSessionId:()=> 'session',getSessionFile:()=>undefined,buildSessionProjection:()=>{assert.ok(idle&&!pending,'busy state scanned native history');scans++;return {messages}}},ui:{notify(){}}};
const setup=()=>{hooks.clear();bridge({on:(name,handler)=>hooks.set(name,handler),registerCommand(){},getThinkingLevel:()=>"medium"})};
const fire=async(name,event={})=>{assert.ok(hooks.has(name),name);await hooks.get(name)(event,ctx)};
const expect=async(state,id)=>{for(let i=0;i<300;i++){const last=statuses.at(-1);if(last?.agent_state===state&&last.leaf_id===id)return;await new Promise(resolve=>setTimeout(resolve,5))}assert.fail('missing native observation '+state+' '+id)};
setup();await fire('session_start',{reason:'startup'});await expect('idle','good');
const initial=statuses.length;const initialScans=scans;tick();tick();assert.equal(scans,initialScans,'unchanged leaf was reprojected');
leaf='aborted';messages=[{role:'assistant',stopReason:'aborted',content:'native-private-aborted'}];await fire('session_tree');await expect('failed','aborted');
leaf='good';messages=[{role:'assistant',stopReason:'stop',content:'native-private-good'}];await fire('session_tree');await expect('idle','good');
leaf='user';messages.push({role:'user',content:'native-private-prompt'});await fire('session_tree');await expect('idle','user');
leaf='length';messages=[{role:'assistant',stopReason:'length'}];await fire('session_tree');await expect('idle','length');
leaf='failed-tool';messages=[{role:'toolResult',isError:true}];await fire('session_tree');await expect('failed','failed-tool');
leaf='successful-tool';messages=[{role:'toolResult',isError:false}];await fire('session_tree');await expect('idle','successful-tool');
leaf=null;messages=[];await fire('session_tree');await expect('idle','-');
if(hooks.has('agent_start')){idle=false;leaf='missed-settle';await fire('agent_start');tick();await expect('active','missed-settle');const completions=events.filter(x=>x.type==='agent.settled').length;idle=true;leaf='missed-final';messages=[{role:'assistant',stopReason:'stop'}];tick();await expect('idle','missed-final');assert.equal(events.filter(x=>x.type==='agent.settled').length,completions,'idle observation invented completion')}
// Retry/compaction/queued continuations are native busy states even after an
// inner response failed. A timer reconciles state; its age proves nothing.
leaf='retry';idle=false;messages=[{role:'assistant',stopReason:'error',content:'native-private-error'}];tick();await expect('active','retry');
idle=true;pending=true;tick();await expect('active','retry');
pending=false;leaf='recovered';messages=[{role:'assistant',stopReason:'stop'}];await fire('agent_settled');await expect('idle','recovered');
leaf='failed-before-response';messages=[{role:'user',content:'native-private-input'}];await fire('agent_before_settle',{outcome:'error'});await fire('agent_settled');await expect('failed','failed-before-response');
leaf='recovered';messages=[{role:'assistant',stopReason:'stop'}];await fire('session_tree');await expect('idle','recovered');
leaf='compacted';idle=false;await fire('session_compact');await expect('active','compacted');
idle=true;tick();await expect('idle','compacted');
// Changes made through an SDK command are reconciled even without a tree hook.
leaf='sdk-leaf';tick();await expect('idle','sdk-leaf');
if(server){const stalled=connect(process.env.WI_AGENT_CONTROL_SOCKET);stalled.on('error',()=>{});await new Promise(resolve=>stalled.on('connect',resolve))}
await fire('session_shutdown',{reason:'reload'});await fire('session_shutdown',{reason:'reload'});assert.ok(timerCleared>0);assert.equal(tick,undefined);
setup();leaf='resume';messages=[{role:'assistant',stopReason:'stop'}];await fire('session_start',{reason:'reload'});await expect('idle','resume');
for(const event of events){assert.equal(event.data,undefined);assert.equal(event.backend_event,undefined);assert.ok(!JSON.stringify(event).includes('native-private'))}
assert.ok(statuses.length>initial);
const previousStops=events.filter(x=>x.type==='runtime.stopping').length;
await fire('session_shutdown',{reason:'quit'});if(server){for(let i=0;i<300&&events.filter(x=>x.type==='runtime.stopping').length===previousStops;i++)await new Promise(resolve=>setTimeout(resolve,5));await new Promise(resolve=>server.close(resolve));}
`, (&url.URL{Scheme: "file", Path: bridge}).String())
			file := filepath.Join(root, "test.mjs")
			if err := os.WriteFile(file, []byte(script), 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, node, "--experimental-strip-types", file)
			cmd.Env = append(os.Environ(), "WI_ID=item", "WI_AGENT_RUNTIME_ID=owner", "WI_AGENT_MODE="+mode, "WI_AGENT_CONTROL_SOCKET="+filepath.Join(root, "control.sock"), "WI_AGENT_EVENT_SOCKET="+filepath.Join(root, "event.sock"))
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("%s branch observation: %v\n%s", mode, err, output)
			}
		})
	}
}
