package runtimepath

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"runtime"
)

// SocketPath resolves the shared host/observer address, including the short
// Darwin fallback. An empty namespace is used only by standalone native stores.
func SocketPath(namespace, itemDir, itemID, runtimeID string, event bool) string {
	relative := ControlSocket(itemID, runtimeID)
	name := "control.sock"
	if event {
		relative = EventSocket(itemID, runtimeID)
		name = "events.sock"
	}
	if namespace == "" {
		namespace = itemDir
		return PortableSocketPath(runtime.GOOS, filepath.Join(itemDir, "agent", name), namespace, relative)
	}
	return PortableSocketPath(runtime.GOOS, filepath.Join(namespace, filepath.FromSlash(relative)), namespace, relative)
}
func PortableSocketPath(goos, candidate, namespace, relative string) string {
	if goos != "darwin" || len(candidate) < 100 {
		return candidate
	}
	digest := sha256.Sum256([]byte(filepath.Clean(namespace)))
	key := fmt.Sprintf("%x", digest[:6])
	return filepath.Join("/tmp", fmt.Sprintf("wi-%d", os.Getuid()), "runtime", key, filepath.FromSlash(relative))
}

func ControlItemDir(itemID string) string {
	digest := sha256.Sum256([]byte(itemID))
	return path.Join("agents", hex.EncodeToString(digest[:6]))
}

func ControlSocket(itemID, runtimeID string) string {
	digest := sha256.Sum256([]byte(runtimeID))
	return path.Join(ControlItemDir(itemID), hex.EncodeToString(digest[:8])+".sock")
}

func EventSocket(itemID, runtimeID string) string {
	digest := sha256.Sum256([]byte(runtimeID))
	return path.Join(ControlItemDir(itemID), hex.EncodeToString(digest[:8])+"-events.sock")
}

func DiagnosticLog(itemID, runtimeID string) string {
	return path.Join("items", itemID, "runtimes", runtimeID, "runtime.log")
}
