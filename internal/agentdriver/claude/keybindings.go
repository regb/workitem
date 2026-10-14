package claude

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"syscall"
)

type inputBindings struct{ submit, sendNow, interrupt []string }

func defaultInputBindings() inputBindings {
	return inputBindings{[]string{"Enter"}, []string{"C-x", "C-s"}, []string{"Escape"}}
}

// Defaults for contexts active at an empty chat editor. Other contexts are
// deliberately not used to drive dialogs, autocomplete or focused panels.
func defaultActiveBindings() map[string]map[string]string {
	return map[string]map[string]string{
		"Chat": {
			"escape": "chat:cancel", "enter": "chat:submit", "ctrl+enter": "chat:sendNow", "ctrl+x ctrl+s": "chat:sendNow",
			"ctrl+l": "chat:clearInput", "cmd+k": "chat:clearScreen", "ctrl+x ctrl+k": "chat:killAgents", "shift+tab": "chat:cycleMode",
			"meta+p": "chat:modelPicker", "meta+o": "chat:fastMode", "meta+t": "chat:thinkingToggle", "ctrl+x enter": "chat:queueSubmit",
			"ctrl+j": "chat:newline", "ctrl+_": "chat:undo", "ctrl+shift+-": "chat:undo", "ctrl+g": "chat:externalEditor", "ctrl+x ctrl+e": "chat:externalEditor",
			"ctrl+s": "chat:stash", "ctrl+v": "chat:imagePaste", "ctrl+x ctrl+a": "abovePrompt:toggle", "ctrl+x tab": "abovePrompt:focus",
			"ctrl+r": "history:search", "up": "history:previous", "down": "history:next",
		},
		"Global": {"ctrl+c": "app:interrupt", "ctrl+d": "app:exit", "ctrl+t": "app:toggleTodos", "ctrl+o": "app:toggleTranscript"},
		"Task":   {"ctrl+b": "task:background", "ctrl+x ctrl+b": "task:background"},
	}
}

// Read a bounded regular file. Missing configuration retains native defaults.
func readInputBindings(path string) (inputBindings, [32]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if os.IsNotExist(err) {
		return defaultInputBindings(), sha256.Sum256(nil), nil
	}
	if err != nil {
		return inputBindings{}, [32]byte{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return inputBindings{}, [32]byte{}, fmt.Errorf("keybindings must be a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(f, (128<<10)+1))
	if err != nil {
		return inputBindings{}, [32]byte{}, err
	}
	if len(data) > 128<<10 {
		return inputBindings{}, [32]byte{}, fmt.Errorf("keybindings exceed 128 KiB")
	}
	fingerprint := sha256.Sum256(data)
	keys, err := parseInputBindings(data)
	return keys, fingerprint, err
}

func parseInputBindings(data []byte) (inputBindings, error) {
	if err := uniqueJSONKeys(data); err != nil {
		return inputBindings{}, err
	}
	var config map[string]json.RawMessage
	if len(bytes.TrimSpace(data)) == 0 || bytes.TrimSpace(data)[0] != '{' || json.Unmarshal(data, &config) != nil {
		return inputBindings{}, fmt.Errorf("invalid keybindings object")
	}
	// Native JSON property names are case-sensitive, unlike Go struct decoding.
	var blocks []map[string]json.RawMessage
	if raw, ok := config["bindings"]; ok {
		if json.Unmarshal(raw, &blocks) != nil {
			return inputBindings{}, fmt.Errorf("invalid bindings array")
		}
	}
	active := defaultActiveBindings()
	seen := map[string]bool{}
	for _, block := range blocks {
		var context string
		if raw, ok := block["context"]; ok {
			if json.Unmarshal(raw, &context) != nil {
				return inputBindings{}, fmt.Errorf("invalid binding context")
			}
		}
		target, ok := active[context]
		if !ok {
			continue
		}
		var overrides map[string]json.RawMessage
		raw, ok := block["bindings"]
		if !ok || json.Unmarshal(raw, &overrides) != nil || overrides == nil {
			return inputBindings{}, fmt.Errorf("keybinding block requires bindings")
		}
		for sequence, raw := range overrides {
			canonical, _, _, err := translateSequence(sequence)
			if err != nil {
				return inputBindings{}, err
			}
			identity := context + ":" + canonical
			if seen[identity] {
				return inputBindings{}, fmt.Errorf("ambiguous duplicate keybinding")
			}
			seen[identity] = true
			action := ""
			if string(raw) != "null" {
				if json.Unmarshal(raw, &action) != nil || action == "" {
					return inputBindings{}, fmt.Errorf("invalid keybinding action")
				}
			}
			// Claude reserves these byte aliases, regardless of user overrides.
			if reservedSequence(canonical) {
				continue
			}
			target[canonical] = action
		}
	}
	choose := func(context, action string, preferred string) []string {
		candidates := []string{}
		for key, bound := range active[context] {
			if bound == action {
				candidates = append(candidates, key)
			}
		}
		sort.Slice(candidates, func(i, j int) bool {
			if candidates[i] == candidates[j] {
				return false
			}
			if candidates[i] == preferred {
				return true
			}
			if candidates[j] == preferred {
				return false
			}
			if len(strings.Fields(candidates[i])) != len(strings.Fields(candidates[j])) {
				return len(strings.Fields(candidates[i])) < len(strings.Fields(candidates[j]))
			}
			return candidates[i] < candidates[j]
		})
		for _, key := range candidates {
			_, keys, portable, err := translateSequence(key)
			if err != nil || !portable {
				continue
			}
			conflict := false
			for otherContext, bindings := range active {
				for other, bound := range bindings {
					if bound == "" || other == key && otherContext == context {
						continue
					}
					if other == key && bound != action || strings.HasPrefix(other, key+" ") || strings.HasPrefix(key, other+" ") {
						conflict = true
					}
				}
			}
			if !conflict {
				return keys
			}
		}
		return nil
	}
	result := inputBindings{submit: choose("Chat", "chat:submit", "enter"), sendNow: choose("Chat", "chat:sendNow", "ctrl+x ctrl+s"), interrupt: choose("Chat", "chat:cancel", "escape")}
	// Ctrl+C is a hardcoded interrupt and cannot be rebound by this file.
	if len(result.interrupt) == 0 {
		result.interrupt = []string{"C-c"}
	}
	return result, nil
}
func reservedSequence(key string) bool {
	switch key {
	case "ctrl+c", "ctrl+d", "ctrl+m", "ctrl+[", "ctrl+i", "ctrl+h":
		return true
	}
	return false
}

func translateSequence(sequence string) (string, []string, bool, error) {
	strokes := strings.Fields(strings.ToLower(sequence))
	if len(strokes) == 0 || len(strokes) > 4 || len(sequence) > 128 {
		return "", nil, false, fmt.Errorf("unsupported keybinding sequence")
	}
	normalized := []string{}
	keys := []string{}
	portable := true
	for _, stroke := range strokes {
		parts := strings.Split(stroke, "+")
		mods := map[string]bool{}
		for _, m := range parts[:len(parts)-1] {
			switch m {
			case "ctrl", "control":
				m = "ctrl"
			case "alt", "opt", "option", "meta":
				m = "meta"
			case "cmd", "command", "super", "win":
				m = "cmd"
			case "shift":
			default:
				return "", nil, false, fmt.Errorf("unknown keybinding modifier")
			}
			if mods[m] {
				return "", nil, false, fmt.Errorf("duplicate keybinding modifier")
			}
			mods[m] = true
		}
		key := parts[len(parts)-1]
		switch key {
		case "esc":
			key = "escape"
		case "return":
			key = "enter"
		}
		if key == "" {
			return "", nil, false, fmt.Errorf("empty keybinding key")
		}
		canonical := ""
		for _, mod := range []string{"ctrl", "meta", "shift", "cmd"} {
			if mods[mod] {
				canonical += mod + "+"
			}
		}
		canonical += key
		normalized = append(normalized, canonical)
		tmuxKey := ""
		switch key {
		case "escape":
			tmuxKey = "Escape"
		case "enter":
			tmuxKey = "Enter"
		case "tab":
			tmuxKey = "Tab"
		case "space":
			tmuxKey = "Space"
		case "up":
			tmuxKey = "Up"
		case "down":
			tmuxKey = "Down"
		case "left":
			tmuxKey = "Left"
		case "right":
			tmuxKey = "Right"
		case "pageup":
			tmuxKey = "PPage"
		case "pagedown":
			tmuxKey = "NPage"
		case "home":
			tmuxKey = "Home"
		case "end":
			tmuxKey = "End"
		case "backspace":
			tmuxKey = "BSpace"
		case "delete":
			tmuxKey = "DC"
		default:
			if len(key) == 1 && key[0] >= 33 && key[0] <= 126 && key != "-" {
				tmuxKey = key
			}
		}
		if tmuxKey == "" || mods["cmd"] {
			portable = false
		}
		if mods["shift"] {
			if !mods["ctrl"] && key == "tab" {
				tmuxKey = "BTab"
			} else if !mods["ctrl"] && len(key) == 1 && key[0] >= 'a' && key[0] <= 'z' {
				tmuxKey = strings.ToUpper(key)
			} else {
				portable = false
			}
		}
		if mods["ctrl"] {
			if mods["shift"] || len(key) != 1 || !(key[0] >= 'a' && key[0] <= 'z') || strings.Contains("cdhimz", key) {
				portable = false
			}
			tmuxKey = "C-" + tmuxKey
		}
		if mods["meta"] {
			tmuxKey = "M-" + tmuxKey
		}
		keys = append(keys, tmuxKey)
	}
	return strings.Join(normalized, " "), keys, portable, nil
}

// Duplicate JSON members lose ordering when decoded into maps. Fail closed
// instead of guessing which value Claude's loader will apply.
func uniqueJSONKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var walk func(int) error
	walk = func(depth int) error {
		if depth > 32 {
			return fmt.Errorf("keybindings nesting exceeds limit")
		}
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delimiter {
		case '{':
			seen := map[string]bool{}
			for decoder.More() {
				key, err := decoder.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				if !ok || seen[name] {
					return fmt.Errorf("duplicate keybindings JSON member")
				}
				seen[name] = true
				if err := walk(depth + 1); err != nil {
					return err
				}
			}
		case '[':
			for decoder.More() {
				if err := walk(depth + 1); err != nil {
					return err
				}
			}
		default:
			return fmt.Errorf("invalid JSON delimiter")
		}
		_, err = decoder.Token()
		return err
	}
	if err := walk(0); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return fmt.Errorf("one keybindings object required")
	}
	return nil
}
