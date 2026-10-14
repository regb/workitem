package claude

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	protocol "github.com/regb/workitem/agentdriver/protocol"
)

func TestKeybindingDefaultsAndUnrelatedCustomization(t *testing.T) {
	for _, data := range []string{`{}`, `{"bindings":[]}`, `{"Bindings":[{"context":"Chat","bindings":{"enter":null}}]}`, `{"bindings":[{"Context":"Chat","bindings":{"enter":null}}]}`, `{"bindings":[{"context":"Chat","bindings":{"ctrl+e":"chat:externalEditor","ctrl+s":null}},{"context":"Confirmation","bindings":{"enter":null}}]}`} {
		keys, err := parseInputBindings([]byte(data))
		if err != nil || !reflect.DeepEqual(keys, defaultInputBindings()) {
			t.Fatalf("%+v %v", keys, err)
		}
	}
}
func TestCustomKeybindingResolutionAndActionAvailability(t *testing.T) {
	raw := `{"bindings":[{"context":"Chat","bindings":{"Enter":null,"CTRL+X CONTROL+S":null,"ctrl+enter":null,"Esc":null,"control+u":"chat:submit","alt+y ctrl+p":"chat:sendNow","ctrl+q":"chat:cancel"}}]}`
	keys, err := parseInputBindings([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	want := inputBindings{[]string{"C-u"}, []string{"M-y", "C-p"}, []string{"C-q"}}
	if !reflect.DeepEqual(keys, want) {
		t.Fatalf("%+v", keys)
	}
	keys, err = parseInputBindings([]byte(`{"bindings":[{"context":"Chat","bindings":{"enter":null}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	input := &tmuxInput{bindingPath: "test", bindings: keys}
	if input.Supports(protocol.CommandPrompt) || input.Supports(protocol.CommandFollowUp) || input.Supports(protocol.CommandSteer) || !input.Supports(protocol.CommandAbort) {
		t.Fatal("disabled unrelated abort or advertised missing submit")
	}
	keys, err = parseInputBindings([]byte(`{"bindings":[{"context":"Chat","bindings":{"ctrl+x ctrl+s":null,"ctrl+enter":null}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	input.bindings = keys
	if !input.Supports(protocol.CommandPrompt) || !input.Supports(protocol.CommandFollowUp) || input.Supports(protocol.CommandSteer) || !input.Supports(protocol.CommandAbort) {
		t.Fatal("send-now unbind disabled unrelated actions")
	}
	keys, err = parseInputBindings([]byte(`{"bindings":[{"context":"Chat","bindings":{"escape":null}}]}`))
	if err != nil || !reflect.DeepEqual(keys.interrupt, []string{"C-c"}) {
		t.Fatal(keys, err)
	}
}
func TestBindingConflictsAndUnrepresentableShortcuts(t *testing.T) {
	for _, raw := range []string{
		`{"bindings":[{"context":"Chat","bindings":{"enter":null,"super+p":"chat:submit"}}]}`,
		`{"bindings":[{"context":"Chat","bindings":{"enter":null,"ctrl+u":"chat:submit","ctrl+u ctrl+y":"chat:stash"}}]}`,
		`{"bindings":[{"context":"Chat","bindings":{"enter":null,"ctrl+u":"chat:submit"}},{"context":"Global","bindings":{"ctrl+u":"app:exit"}}]}`,
		`{"bindings":[{"context":"Chat","bindings":{"enter":null,"ctrl+z":"chat:submit"}}]}`,
	} {
		keys, err := parseInputBindings([]byte(raw))
		if err != nil || len(keys.submit) != 0 || len(keys.interrupt) == 0 {
			t.Fatal(keys, err)
		}
	}
	keys, err := parseInputBindings([]byte(`{"bindings":[{"context":"Chat","bindings":{"ctrl+enter":"chat:sendNow"}}]}`))
	if err != nil || !reflect.DeepEqual(keys.sendNow, []string{"C-x", "C-s"}) {
		t.Fatal(keys, err)
	}
}
func TestMalformedAndDuplicateKeybindingsFailClosed(t *testing.T) {
	for _, raw := range []string{"", `[]`, `null`, `{} {}`, `{"bindings":[{"context":"Chat","bindings":{"enter":4}}]}`, `{"bindings":[{"context":"Chat","bindings":{"enter":null,"enter":"chat:submit"}}]}`, `{"bindings":[{"context":"Chat","bindings":{"esc":null,"escape":"chat:cancel"}}]}`, `{"bindings":[{"context":"Chat","bindings":{"ctl+u":"chat:submit"}}]}`} {
		if _, err := parseInputBindings([]byte(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}
func TestShortcutTranslation(t *testing.T) {
	for raw, want := range map[string][]string{"control+K option+s": {"C-k", "M-s"}, "RETURN": {"Enter"}, "shift+k": {"K"}, "SHIFT+TAB": {"BTab"}, "meta+left": {"M-Left"}} {
		_, keys, portable, err := translateSequence(raw)
		if err != nil || !portable || !reflect.DeepEqual(keys, want) {
			t.Fatalf("%s %v %v %v", raw, keys, portable, err)
		}
	}
	for _, raw := range []string{"ctrl+enter", "cmd+k", "ctrl+shift+c", "ctrl+z", "wheelup"} {
		_, _, portable, err := translateSequence(raw)
		if err != nil || portable {
			t.Fatalf("%s %v %v", raw, portable, err)
		}
	}
}
func TestCustomKeysTransportAndLiveChangesFence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keybindings.json")
	raw := `{"bindings":[{"context":"Chat","bindings":{"enter":null,"ctrl+x ctrl+s":null,"escape":null,"ctrl+u":"chat:submit","ctrl+p ctrl+y":"chat:sendNow","meta+q":"chat:cancel"}}]}`
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	keys, fingerprint, err := readInputBindings(path)
	if err != nil {
		t.Fatal(err)
	}
	calls := []string{}
	screen := "───\n❯\n───"
	payload := ""
	input := &tmuxInput{pane: "%7", owner: syscall.Getpgrp(), bindingPath: path, bindings: keys, fingerprint: fingerprint, run: func(_ context.Context, reader io.Reader, args ...string) (string, error) {
		if reader != nil {
			data, _ := io.ReadAll(reader)
			payload = string(data)
		}
		calls = append(calls, strings.Join(args, " "))
		if args[0] == "display-message" {
			return fmt.Sprintf("%%7|%d|0|0|0", syscall.Getpgrp()), nil
		}
		if args[0] == "capture-pane" {
			return screen, nil
		}
		if args[0] == "paste-buffer" {
			screen = "───\n❯ " + payload + "\n───"
		}
		if args[0] == "send-keys" {
			screen = "───\n❯\n───"
		}
		return "", nil
	}}
	for _, steer := range []bool{false, true} {
		if err := input.Send(context.Background(), "private prompt", steer); err != nil {
			t.Fatal(err)
		}
	}
	if err := input.Interrupt(context.Background()); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(calls, "\n")
	for _, want := range []string{"send-keys -t %7 C-u", "send-keys -t %7 C-p C-y", "send-keys -t %7 M-q"} {
		if !strings.Contains(joined, want) {
			t.Fatal(joined)
		}
	}
	if err := os.WriteFile(path, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	calls = nil
	if err := input.Send(context.Background(), "work", false); err == nil {
		t.Fatal("used stale keys")
	}
	if len(calls) != 0 {
		t.Fatal("touched pane after file change")
	}
}
func TestKeybindingFileBoundsAndMissingDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keybindings.json")
	keys, _, err := readInputBindings(path)
	if err != nil || !reflect.DeepEqual(keys, defaultInputBindings()) {
		t.Fatal(keys, err)
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("x", (128<<10)+1)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readInputBindings(path); err == nil {
		t.Fatal("accepted oversized file")
	}
	if _, _, err := readInputBindings(filepath.Dir(path)); err == nil {
		t.Fatal("accepted directory")
	}
	fifo := filepath.Join(filepath.Dir(path), "fifo")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readInputBindings(fifo); err == nil {
		t.Fatal("accepted named pipe")
	}
}
