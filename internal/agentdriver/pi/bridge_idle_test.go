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

func TestHeadlessBridgeChecksIdleInsidePiWithoutOpeningSockets(t *testing.T) {
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
	script := fmt.Sprintf(`import bridge from %q;
import assert from 'node:assert/strict';
import fs from 'node:fs';
import {syncBuiltinESMExports} from 'node:module';
let handler;let idle=false;let pending=false;let stopped=false;const results=[];
fs.writeSync=(fd,data)=>{assert.equal(fd,1);results.push(JSON.parse(data));return data.length};syncBuiltinESMExports();
bridge({registerCommand:(name,command)=>{assert.equal(name,'wi-control-idle');handler=command.handler},on:()=>{}});
const ctx={isIdle:()=>idle,hasPendingMessages:()=>pending,shutdown:()=>{stopped=true},abort:()=>{}};
const command={protocol_version:1,runtime_id:'owner',work_item_id:'item',id:'command',require_idle:true,type:'shutdown'};
const encode=cmd=>Buffer.from(JSON.stringify(cmd)).toString('base64');
await handler(encode(command),ctx);assert.equal(results.at(-1).success,false);assert.match(results.at(-1).error,/became busy/);assert.equal(stopped,false);
assert.equal(results.at(-1).type,'wi_control_result');
idle=true;pending=true;
await handler(encode(command),ctx);assert.equal(results.at(-1).success,false);assert.match(results.at(-1).error,/became busy/);assert.equal(stopped,false);
pending=false;
await handler(encode({...command,runtime_id:'superseded'}),ctx);assert.match(results.at(-1).error,/owner mismatch/);
await handler(encode(command),ctx);assert.equal(stopped,true);assert.equal(results.at(-1).success,true);
`, (&url.URL{Scheme: "file", Path: bridge}).String())
	path := filepath.Join(root, "test.mjs")
	if err := os.WriteFile(path, []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, "--experimental-strip-types", path)
	cmd.Env = append(os.Environ(), "WI_ID=item", "WI_AGENT_RUNTIME_ID=owner", "WI_AGENT_MODE=headless", "WI_AGENT_CONTROL_SOCKET="+filepath.Join(root, "control.sock"), "WI_AGENT_EVENT_SOCKET="+filepath.Join(root, "event.sock"))
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("headless bridge %v\n%s", err, output)
	}
	if _, err := os.Stat(filepath.Join(root, "control.sock")); !os.IsNotExist(err) {
		t.Fatal("headless bridge opened control socket")
	}
}
