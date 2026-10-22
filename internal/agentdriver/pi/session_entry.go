package pi

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
)

// sessionEntryTail bounds how much of a session file is searched for a just
// appended entry.
const sessionEntryTail = 2 << 20

// sessionEntryID returns the id of the session entry holding a message, found
// by the role and millisecond timestamp Pi records both in its events and in
// the entry. Only the end of the file is read, newest entries first.
func sessionEntryID(path, role string, timestamp int64) (string, bool) {
	file, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", false
	}
	start := max(info.Size()-sessionEntryTail, 0)
	data, err := io.ReadAll(io.NewSectionReader(file, start, info.Size()-start))
	if err != nil {
		return "", false
	}
	lines := bytes.Split(data, []byte{'\n'})
	for i := len(lines) - 1; i >= 0; i-- {
		var entry struct {
			Type    string `json:"type"`
			ID      string `json:"id"`
			Message struct {
				Role      string `json:"role"`
				Timestamp int64  `json:"timestamp"`
			} `json:"message"`
		}
		if json.Unmarshal(lines[i], &entry) != nil || entry.Type != "message" {
			continue
		}
		if entry.Message.Role == role && entry.Message.Timestamp == timestamp && entry.ID != "" {
			return entry.ID, true
		}
	}
	return "", false
}
