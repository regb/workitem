package direnv

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
)

// Revert restores the environment before direnv loaded the current directory.
// Invalid state is an error rather than silently retaining project secrets.
func Revert(env map[string]string) (map[string]string, error) {
	result := make(map[string]string, len(env))
	for key, value := range env {
		result[key] = value
	}
	if diff := env["DIRENV_DIFF"]; diff != "" {
		raw, err := base64.URLEncoding.DecodeString(diff)
		if err != nil {
			return nil, fmt.Errorf("decode DIRENV_DIFF: %w", err)
		}
		reader, err := zlib.NewReader(bytes.NewReader(raw))
		if err != nil {
			return nil, fmt.Errorf("decode DIRENV_DIFF: %w", err)
		}
		data, err := io.ReadAll(io.LimitReader(reader, 4<<20))
		reader.Close()
		if err != nil {
			return nil, err
		}
		var state struct {
			Previous map[string]*string `json:"p"`
			Next     map[string]*string `json:"n"`
		}
		if err := json.Unmarshal(data, &state); err != nil {
			return nil, fmt.Errorf("decode DIRENV_DIFF: %w", err)
		}
		for key := range state.Next {
			delete(result, key)
		}
		for key, value := range state.Previous {
			if value == nil {
				delete(result, key)
			} else {
				result[key] = *value
			}
		}
	}
	return result, nil
}
