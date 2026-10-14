package cli

import (
	"bytes"
	"encoding/base64"
	protocol "github.com/regb/workitem/agentdriver/protocol"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestControlImageFilesAreBoundedAndDetected(t *testing.T) {
	root := t.TempDir()
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewNRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "image.png"), data.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	images, err := readAgentControlImages(root, []string{"image.png"})
	if err != nil || len(images) != 1 || images[0].MIMEType != "image/png" || images[0].Data != base64.StdEncoding.EncodeToString(data.Bytes()) {
		t.Fatal(images, err)
	}
	if _, err := readAgentControlImages(root, []string{"image.png", "image.png", "image.png", "image.png", "image.png"}); err == nil {
		t.Fatal("accepted five images")
	}
	if err := os.WriteFile(filepath.Join(root, "large"), bytes.Repeat([]byte{0}, protocol.MaxControlImageBytes+1), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readAgentControlImages(root, []string{"large"}); err == nil {
		t.Fatal("accepted oversized file")
	}
	fifo := filepath.Join(root, "fifo")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{fifo, root} {
		if _, err := readAgentControlImages(root, []string{path}); err == nil {
			t.Fatal("accepted nonregular file")
		}
	}
}
