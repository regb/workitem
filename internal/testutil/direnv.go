package testutil

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"testing"
)

// DirenvDiff encodes a DIRENV_DIFF value, zlib-compressed and URL-safe base64
// as direnv writes it, from its JSON payload.
func DirenvDiff(t testing.TB, payload string) string {
	t.Helper()
	var compressed bytes.Buffer
	writer := zlib.NewWriter(&compressed)
	if _, err := writer.Write([]byte(payload)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return base64.URLEncoding.EncodeToString(compressed.Bytes())
}
