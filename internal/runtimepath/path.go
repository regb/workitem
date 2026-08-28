package runtimepath

import (
	"crypto/sha256"
	"encoding/hex"
	"path"
)

func ControlItemDir(itemID string) string {
	digest := sha256.Sum256([]byte(itemID))
	return path.Join("agents", hex.EncodeToString(digest[:6]))
}

func ControlSocket(itemID, runtimeID string) string {
	digest := sha256.Sum256([]byte(runtimeID))
	return path.Join(ControlItemDir(itemID), hex.EncodeToString(digest[:8])+".sock")
}

func DiagnosticLog(itemID, runtimeID string) string {
	return path.Join("items", itemID, "runtimes", runtimeID, "runtime.log")
}
