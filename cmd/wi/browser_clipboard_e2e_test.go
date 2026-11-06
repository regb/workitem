package main

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"time"
)

// Copying runs without navigator.clipboard. Read access is granted afterwards
// only in the fixture's private browser context to inspect the actual result.
func (f *binaryFixture) readBrowserClipboard(cdp, origin string) string {
	f.t.Helper()
	var decoded string
	if json.Unmarshal([]byte(strings.TrimSpace(cdp)), &decoded) == nil {
		cdp = decoded
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	script := `
const socket = new WebSocket(process.argv[1].trim());
const origin = process.argv[2];
let target;
const timer = setTimeout(() => { console.error('clipboard inspection timeout'); process.exit(1) }, 7000);
socket.onerror = () => { console.error('clipboard CDP connection failed'); process.exit(1) };
socket.onopen = () => socket.send(JSON.stringify({id:1,method:'Target.getTargets'}));
socket.onmessage = event => {
  const message = JSON.parse(event.data);
  if (message.error) { console.error(JSON.stringify(message.error)); process.exit(1) }
  if (message.id === 1) {
    target = message.result.targetInfos.find(target => target.type === 'page' && target.url.startsWith(origin+'/'));
    if (!target) { console.error('isolated test page not found'); process.exit(1) }
    const params = {origin,permissions:['clipboardReadWrite','clipboardSanitizedWrite']};
    if (target.browserContextId) params.browserContextId = target.browserContextId;
    socket.send(JSON.stringify({id:2,method:'Browser.grantPermissions',params}));
  } else if (message.id === 2) {
    socket.send(JSON.stringify({id:3,method:'Target.attachToTarget',params:{targetId:target.targetId,flatten:true}}));
  } else if (message.id === 3) {
    socket.send(JSON.stringify({id:4,sessionId:message.result.sessionId,method:'Runtime.evaluate',params:{expression:'navigator.clipboard.readText()',awaitPromise:true,returnByValue:true,userGesture:true}}));
  } else if (message.id === 4) {
    if (message.result.exceptionDetails) { console.error('browser clipboard inspection failed'); process.exit(1) }
    console.log(JSON.stringify(message.result.result.value));
    clearTimeout(timer);
    socket.close();
  }
};`
	command := exec.CommandContext(ctx, "node", "-e", script, strings.TrimSpace(cdp), origin)
	command.Env = f.env
	out, err := command.CombinedOutput()
	if err != nil {
		f.t.Fatalf("clipboard inspection: %v %s", err, out)
	}
	if err := json.Unmarshal(out, &decoded); err != nil {
		f.t.Fatalf("invalid clipboard inspection response: %v", err)
	}
	return decoded
}
