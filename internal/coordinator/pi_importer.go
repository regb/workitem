package coordinator

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/regb/workitem/internal/model"
)

const PiSessionProjection = "pi_sessions"

type PiEventFact struct {
	Line         uint64    `json:"line"`
	Type         string    `json:"type,omitempty"`
	Role         string    `json:"role,omitempty"`
	Timestamp    time.Time `json:"timestamp,omitempty"`
	StopReason   string    `json:"stop_reason,omitempty"`
	ContentTypes []string  `json:"content_types,omitempty"`
	Terminal     bool      `json:"terminal,omitempty"`
	Failed       bool      `json:"failed,omitempty"`
}

type PiSessionIndex struct {
	WorkItemID            string       `json:"work_item_id"`
	SessionID             string       `json:"session_id"`
	Source                string       `json:"source"`
	Offset                int64        `json:"offset"`
	Size                  int64        `json:"size"`
	ObservedAt            time.Time    `json:"observed_at"`
	EntriesScanned        uint64       `json:"entries_scanned"`
	MalformedLines        uint64       `json:"malformed_lines"`
	InferredTurnState     string       `json:"inferred_turn_state"`
	LastEvent             *PiEventFact `json:"last_event,omitempty"`
	LastTurnActivity      *PiEventFact `json:"last_turn_activity,omitempty"`
	LastUserPrompt        *PiEventFact `json:"last_user_prompt,omitempty"`
	LastAssistantMessage  *PiEventFact `json:"last_assistant_message,omitempty"`
	LastTerminalAssistant *PiEventFact `json:"last_terminal_assistant,omitempty"`
	LastToolActivity      *PiEventFact `json:"last_tool_activity,omitempty"`
}

type PiImportReport struct {
	FilesSeen      int      `json:"files_seen"`
	FilesImported  int      `json:"files_imported"`
	EntriesScanned uint64   `json:"entries_scanned"`
	MalformedLines uint64   `json:"malformed_lines"`
	BytesRead      int64    `json:"bytes_read"`
	Warnings       []string `json:"warnings,omitempty"`
}

type piLineHeader struct {
	Type       string `json:"type"`
	Timestamp  string `json:"timestamp"`
	StopReason string `json:"stopReason"`
	Message    struct {
		Role       string          `json:"role"`
		Content    json.RawMessage `json:"content"`
		StopReason string          `json:"stopReason"`
	} `json:"message"`
}

type piContentHeader struct {
	Type string          `json:"type"`
	Text json.RawMessage `json:"text"`
}

func ImportPiSessions(ctx context.Context, database *Database, dataRoot string) (PiImportReport, error) {
	return importPiSessions(ctx, database, dataRoot, false)
}

func ImportActivePiSessions(ctx context.Context, database *Database, dataRoot string) (PiImportReport, error) {
	return importPiSessions(ctx, database, dataRoot, true)
}

func importPiSessions(ctx context.Context, database *Database, dataRoot string, activeOnly bool) (PiImportReport, error) {
	report := PiImportReport{Warnings: []string{}}
	manifests, err := database.ListManifests()
	if err != nil {
		return report, err
	}
	sort.Slice(manifests, func(i, j int) bool { return manifests[i].ID < manifests[j].ID })
	for _, manifest := range manifests {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		if manifest.RootPiSession == nil || activeOnly && manifest.State != model.StateWorking && manifest.State != model.StateWaiting {
			continue
		}
		relativeSession := filepath.Clean(manifest.RootPiSession.Path)
		if filepath.IsAbs(relativeSession) || relativeSession == "." || relativeSession == ".." || strings.HasPrefix(relativeSession, ".."+string(filepath.Separator)) {
			report.Warnings = append(report.Warnings, fmt.Sprintf("%s: invalid Pi session path", manifest.ID))
			continue
		}
		source := filepath.Join("items", manifest.ID, relativeSession)
		if !validSourceKey(source) {
			report.Warnings = append(report.Warnings, fmt.Sprintf("%s: invalid Pi session source", manifest.ID))
			continue
		}
		path := filepath.Join(dataRoot, source)
		info, err := os.Lstat(path)
		if err != nil {
			if !os.IsNotExist(err) {
				report.Warnings = append(report.Warnings, fmt.Sprintf("%s: inspect Pi session: %v", manifest.ID, err))
			}
			continue
		}
		report.FilesSeen++
		if !info.Mode().IsRegular() {
			report.Warnings = append(report.Warnings, fmt.Sprintf("%s: Pi session is not a regular file", manifest.ID))
			continue
		}
		part, err := importPiSessionFile(ctx, database, path, source, manifest.ID, manifest.RootPiSession.ID, info)
		report.EntriesScanned += part.EntriesScanned
		report.MalformedLines += part.MalformedLines
		report.BytesRead += part.BytesRead
		report.Warnings = append(report.Warnings, part.Warnings...)
		if err != nil {
			report.Warnings = append(report.Warnings, fmt.Sprintf("%s: %v", manifest.ID, err))
			continue
		}
		report.FilesImported++
	}
	return report, nil
}

func importPiSessionFile(ctx context.Context, database *Database, path, source, itemID, sessionID string, info os.FileInfo) (PiImportReport, error) {
	report := PiImportReport{Warnings: []string{}}
	file, err := os.Open(path)
	if err != nil {
		return report, err
	}
	defer file.Close()
	checkpoint, found, err := database.SourceCheckpoint(source)
	if err != nil {
		return report, err
	}
	fingerprintSize := info.Size()
	if fingerprintSize > 4096 {
		fingerprintSize = 4096
	}
	if found && checkpoint.FingerprintSize < fingerprintSize {
		fingerprintSize = checkpoint.FingerprintSize
	}
	fingerprint, err := fileFingerprint(file, fingerprintSize)
	if err != nil {
		return report, err
	}
	reset := !found || checkpoint.Fingerprint != fingerprint || checkpoint.Offset > info.Size()
	if reset {
		checkpoint = SourceCheckpoint{Source: source, Fingerprint: fingerprint, FingerprintSize: fingerprintSize}
	}
	index := PiSessionIndex{WorkItemID: itemID, SessionID: sessionID, Source: source}
	if !reset {
		_, _ = database.ReadProjection(PiSessionProjection, itemID, &index)
	}
	attention := AttentionActivity{WorkItemID: itemID}
	_, _ = database.ReadProjection(AttentionActivityProjection, itemID, &attention)
	if _, err := file.Seek(checkpoint.Offset, io.SeekStart); err != nil {
		return report, err
	}
	reader := bufio.NewReaderSize(file, 64*1024)
	offset, batchStart := checkpoint.Offset, checkpoint.Offset
	changed := false
	commit := func() error {
		if offset == checkpoint.Offset && !changed {
			return nil
		}
		observed := time.Now().UTC()
		checkpoint.Offset = offset
		checkpoint.Size = info.Size()
		checkpoint.ModifiedAt = info.ModTime().UTC()
		checkpoint.ObservedAt = observed
		index.Offset = offset
		index.Size = info.Size()
		index.ObservedAt = observed
		index.InferredTurnState = inferIndexedPiTurnState(index)
		attention.ObservedAt = observed
		attention.Source = source
		indexBytes, _ := json.Marshal(index)
		attentionBytes, _ := json.Marshal(attention)
		_, err := database.CommitSourceBatch(checkpoint, nil, []ProjectionUpdate{{Projection: PiSessionProjection, Key: itemID, Value: indexBytes}, {Projection: AttentionActivityProjection, Key: itemID, Value: attentionBytes}})
		if err != nil {
			return err
		}
		batchStart = offset
		changed = false
		return nil
	}
	for {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		line, consumed, complete, oversized, err := readBoundedJSONLine(reader, 16<<20)
		if err != nil {
			return report, err
		}
		if !complete {
			break
		}
		offset += consumed
		report.BytesRead += consumed
		checkpoint.RecordsSeen++
		if oversized {
			report.MalformedLines++
			checkpoint.RecordsSkipped++
		} else if fact, ok := summarizePiFact(checkpoint.RecordsSeen, line); ok {
			report.EntriesScanned++
			checkpoint.RecordsImported++
			applyPiFact(&index, &attention, fact)
		} else {
			report.MalformedLines++
			checkpoint.RecordsSkipped++
		}
		index.EntriesScanned = checkpoint.RecordsImported
		index.MalformedLines = checkpoint.RecordsSkipped
		changed = true
		if checkpoint.RecordsSeen%runtimeBatchRecords == 0 || offset-batchStart >= runtimeBatchBytes {
			if err := commit(); err != nil {
				return report, err
			}
		}
	}
	if err := commit(); err != nil {
		return report, err
	}
	return report, nil
}

func summarizePiFact(line uint64, raw []byte) (PiEventFact, bool) {
	var value piLineHeader
	if json.Unmarshal(raw, &value) != nil {
		return PiEventFact{}, false
	}
	fact := PiEventFact{Line: line, Type: compactPiMetadata(value.Type, 128), Role: compactPiMetadata(value.Message.Role, 32), StopReason: compactPiStopReason(firstNonEmptyCoordinator(value.Message.StopReason, value.StopReason))}
	if value.Timestamp != "" {
		fact.Timestamp, _ = time.Parse(time.RFC3339Nano, value.Timestamp)
	}
	content := bytes.TrimSpace(value.Message.Content)
	hasText, hasTool := false, false
	if len(content) > 0 && !bytes.Equal(content, []byte("null")) {
		if content[0] == '"' {
			hasText = len(content) > 2
			fact.ContentTypes = []string{"text"}
		} else {
			var blocks []piContentHeader
			if json.Unmarshal(content, &blocks) != nil {
				return PiEventFact{}, false
			}
			seen := map[string]bool{}
			for _, block := range blocks {
				block.Type = compactPiMetadata(block.Type, 64)
				if block.Type != "" && !seen[block.Type] {
					seen[block.Type] = true
					fact.ContentTypes = append(fact.ContentTypes, block.Type)
				}
				hasText = hasText || block.Type == "text" && len(bytes.TrimSpace(block.Text)) > 2
				hasTool = hasTool || block.Type == "toolCall" || block.Type == "bashExecution"
			}
		}
	}
	stop := strings.ToLower(strings.TrimSpace(fact.StopReason))
	fact.Failed = stop == "error" || stop == "errored" || stop == "failed" || stop == "aborted" || stop == "abort" || stop == "cancelled" || stop == "canceled" || stop == "length"
	if fact.Role == "assistant" {
		fact.Terminal = fact.Failed || !hasTool && hasText && (stop == "" || stop == "stop" || stop == "end_turn" || stop == "complete" || stop == "completed")
	}
	return fact, true
}

func applyPiFact(index *PiSessionIndex, attention *AttentionActivity, fact PiEventFact) {
	copyFact := fact
	index.LastEvent = &copyFact
	turnActivity := fact.Role == "user" || fact.Role == "assistant" || fact.Role == "toolResult" || fact.Type == "toolResult" || containsCoordinator(fact.ContentTypes, "toolCall", "toolResult", "bashExecution")
	if turnActivity {
		index.LastTurnActivity = &copyFact
	}
	switch {
	case fact.Role == "user":
		index.LastUserPrompt = &copyFact
		if !fact.Timestamp.IsZero() && (attention.LastRequestedAt == nil || fact.Timestamp.After(*attention.LastRequestedAt)) {
			at := fact.Timestamp.UTC()
			attention.LastRequestedAt = &at
		}
	case fact.Role == "assistant":
		index.LastAssistantMessage = &copyFact
		if fact.Terminal || fact.Failed {
			index.LastTerminalAssistant = &copyFact
			if !fact.Timestamp.IsZero() && (attention.LastCompletedAt == nil || fact.Timestamp.After(*attention.LastCompletedAt)) {
				at := fact.Timestamp.UTC()
				attention.LastCompletedAt = &at
			}
		}
	case fact.Role == "toolResult" || fact.Type == "toolResult" || containsCoordinator(fact.ContentTypes, "toolCall", "toolResult", "bashExecution"):
		index.LastToolActivity = &copyFact
	}
	if fact.Role == "assistant" && containsCoordinator(fact.ContentTypes, "toolCall", "bashExecution") {
		index.LastToolActivity = &copyFact
	}
}

func inferIndexedPiTurnState(index PiSessionIndex) string {
	if index.LastTurnActivity == nil {
		return "idle"
	}
	if terminal := index.LastTerminalAssistant; terminal != nil {
		if (index.LastUserPrompt == nil || !index.LastUserPrompt.Timestamp.After(terminal.Timestamp)) && (index.LastToolActivity == nil || !index.LastToolActivity.Timestamp.After(terminal.Timestamp)) {
			if terminal.Failed {
				return "failed"
			}
			return "idle"
		}
	}
	return "incomplete"
}

func compactPiStopReason(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "", "stop", "end_turn", "complete", "completed", "error", "errored", "failed", "aborted", "abort", "cancelled", "canceled", "length":
		return value
	default:
		return "other"
	}
}

func compactPiMetadata(value string, limit int) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if len(value) > limit {
		return "other"
	}
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-' {
			continue
		}
		return "other"
	}
	return value
}

func containsCoordinator(values []string, needles ...string) bool {
	for _, value := range values {
		for _, needle := range needles {
			if value == needle {
				return true
			}
		}
	}
	return false
}

func firstNonEmptyCoordinator(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
