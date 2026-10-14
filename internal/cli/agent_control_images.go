package cli

import (
	"encoding/base64"
	"fmt"
	protocol "github.com/regb/workitem/agentdriver/protocol"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
)

func readAgentControlImages(cwd string, paths []string) ([]protocol.ImageContent, error) {
	if len(paths) > protocol.MaxControlImages {
		return nil, fmt.Errorf("at most four control images are allowed")
	}
	images := []protocol.ImageContent{}
	total := 0
	for _, path := range paths {
		if !filepath.IsAbs(path) {
			path = filepath.Join(cwd, path)
		}
		f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
		if err != nil {
			return nil, err
		}
		info, err := f.Stat()
		if err != nil || !info.Mode().IsRegular() {
			f.Close()
			return nil, fmt.Errorf("control image must be a regular file")
		}
		data, err := io.ReadAll(io.LimitReader(f, int64(protocol.MaxControlImageBytes-total)+1))
		f.Close()
		if err != nil {
			return nil, err
		}
		total += len(data)
		if total > protocol.MaxControlImageBytes {
			return nil, fmt.Errorf("control images exceed 8 MiB total")
		}
		images = append(images, protocol.ImageContent{MIMEType: http.DetectContentType(data), Data: base64.StdEncoding.EncodeToString(data)})
	}
	if err := (protocol.ControlCommand{Type: protocol.CommandPrompt, Images: images}).ValidateContent(); err != nil {
		return nil, err
	}
	return images, nil
}
