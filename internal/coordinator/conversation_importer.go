package coordinator

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/regb/workitem/internal/model"
)

const ConversationProjection = "conversations"

type ConversationEventFact struct {
	Line         uint64    `json:"line"`
	Type         string    `json:"type,omitempty"`
	Role         string    `json:"role,omitempty"`
	Timestamp    time.Time `json:"timestamp,omitempty"`
	StopReason   string    `json:"stop_reason,omitempty"`
	ContentTypes []string  `json:"content_types,omitempty"`
	Terminal     bool      `json:"terminal,omitempty"`
	Failed       bool      `json:"failed,omitempty"`
}

type ConversationIndex struct {
	WorkItemID            string                 `json:"work_item_id"`
	ConversationID        string                 `json:"conversation_id"`
	Source                string                 `json:"source"`
	Offset                int64                  `json:"offset"`
	Size                  int64                  `json:"size"`
	ObservedAt            time.Time              `json:"observed_at"`
	EntriesScanned        uint64                 `json:"entries_scanned"`
	MalformedLines        uint64                 `json:"malformed_lines"`
	InferredTurnState     string                 `json:"inferred_turn_state"`
	LastEvent             *ConversationEventFact `json:"last_event,omitempty"`
	LastTurnActivity      *ConversationEventFact `json:"last_turn_activity,omitempty"`
	LastUserPrompt        *ConversationEventFact `json:"last_user_prompt,omitempty"`
	LastAssistantMessage  *ConversationEventFact `json:"last_assistant_message,omitempty"`
	LastTerminalAssistant *ConversationEventFact `json:"last_terminal_assistant,omitempty"`
	LastToolActivity      *ConversationEventFact `json:"last_tool_activity,omitempty"`
}

type ConversationRecord struct {
	Line uint64
	Raw  []byte
}

type ConversationDecoder func(context.Context, model.Conversation, []ConversationRecord) ([]ConversationEventFact, []bool, error)
type ConversationDecoders map[string]ConversationDecoder

type ConversationImportReport struct {
	FilesSeen      int      `json:"files_seen"`
	FilesImported  int      `json:"files_imported"`
	EntriesScanned uint64   `json:"entries_scanned"`
	MalformedLines uint64   `json:"malformed_lines"`
	BytesRead      int64    `json:"bytes_read"`
	Warnings       []string `json:"warnings,omitempty"`
}

func ImportConversations(ctx context.Context, database *Database, dataRoot string, registries ...ConversationDecoders) (ConversationImportReport, error) {
	return importConversations(ctx, database, dataRoot, false, firstConversationDecoders(registries), noLock{})
}

func ImportActiveConversations(ctx context.Context, database *Database, dataRoot string, registries ...ConversationDecoders) (ConversationImportReport, error) {
	return importConversations(ctx, database, dataRoot, true, firstConversationDecoders(registries), noLock{})
}

// noLock serves callers that already hold the domain lock or run before the
// daemon serves anyone.
type noLock struct{}

func (noLock) Lock()   {}
func (noLock) Unlock() {}

func firstConversationDecoders(registries []ConversationDecoders) ConversationDecoders {
	if len(registries) > 0 {
		return registries[0]
	}
	return nil
}

// importConversations holds apply only while folding decoded facts into
// projections, so a running daemon keeps ingesting runtime events while
// drivers decode.
func importConversations(ctx context.Context, database *Database, dataRoot string, activeOnly bool, decoders ConversationDecoders, apply sync.Locker) (ConversationImportReport, error) {
	report := ConversationImportReport{Warnings: []string{}}
	manifests, err := database.ListManifests()
	if err != nil {
		return report, err
	}
	sort.Slice(manifests, func(i, j int) bool { return manifests[i].ID < manifests[j].ID })
	for _, manifest := range manifests {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		if manifest.PrimaryConversation == nil || activeOnly && manifest.State != model.StateActive {
			continue
		}
		relativeSession := filepath.Clean(manifest.PrimaryConversation.NativeLocator())
		if filepath.IsAbs(relativeSession) || relativeSession == "." || relativeSession == ".." || strings.HasPrefix(relativeSession, ".."+string(filepath.Separator)) {
			report.Warnings = append(report.Warnings, fmt.Sprintf("%s: invalid conversation path", manifest.ID))
			continue
		}
		source := filepath.Join("items", manifest.ID, relativeSession)
		if !validSourceKey(source) {
			report.Warnings = append(report.Warnings, fmt.Sprintf("%s: invalid conversation source", manifest.ID))
			continue
		}
		path := filepath.Join(dataRoot, source)
		info, err := os.Lstat(path)
		if err != nil {
			if !os.IsNotExist(err) {
				report.Warnings = append(report.Warnings, fmt.Sprintf("%s: inspect conversation: %v", manifest.ID, err))
			}
			continue
		}
		report.FilesSeen++
		if !info.Mode().IsRegular() {
			report.Warnings = append(report.Warnings, fmt.Sprintf("%s: conversation is not a regular file", manifest.ID))
			continue
		}
		decoder := decoders[manifest.PrimaryConversation.Driver]
		part, err := importConversationFile(ctx, database, path, source, manifest.ID, *manifest.PrimaryConversation, info, decoder, apply)
		report.EntriesScanned += part.EntriesScanned
		report.MalformedLines += part.MalformedLines
		report.BytesRead += part.BytesRead
		report.Warnings = append(report.Warnings, part.Warnings...)
		if errors.Is(err, errImportRaced) {
			continue
		}
		if err != nil {
			report.Warnings = append(report.Warnings, fmt.Sprintf("%s: %v", manifest.ID, err))
			continue
		}
		report.FilesImported++
	}
	return report, nil
}

// errImportRaced stops a file's import when another import advanced its
// checkpoint meanwhile; that import already covers the records.
var errImportRaced = errors.New("conversation checkpoint changed during import")

// importConversationFile reads and decodes new records without the domain
// lock: decoding runs the driver, and a large catch-up takes long. Only
// folding the facts into projections and committing holds apply, after
// checking that no other import moved the checkpoint since it was read.
func importConversationFile(ctx context.Context, database *Database, path, source, itemID string, conversation model.Conversation, info os.FileInfo, decoder ConversationDecoder, apply sync.Locker) (ConversationImportReport, error) {
	report := ConversationImportReport{Warnings: []string{}}
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
	// committed is the checkpoint as stored; the import applies only on top
	// of it.
	committed, committedFound := checkpoint, found
	reset := !found || checkpoint.Fingerprint != fingerprint || checkpoint.Offset > info.Size()
	if !reset {
		var index ConversationIndex
		if found, err := database.ReadProjection(ConversationProjection, itemID, &index); err != nil {
			return report, err
		} else if !found {
			reset = true
		}
	}
	if reset {
		checkpoint = SourceCheckpoint{Source: source, Fingerprint: fingerprint, FingerprintSize: fingerprintSize}
	}
	if _, err := file.Seek(checkpoint.Offset, io.SeekStart); err != nil {
		return report, err
	}
	reader := bufio.NewReaderSize(file, 64*1024)
	offset, batchStart := checkpoint.Offset, checkpoint.Offset
	changed := false
	records := make([]ConversationRecord, 0, runtimeBatchRecords)
	facts := []ConversationEventFact{}
	flush := func() error {
		if len(records) == 0 {
			return nil
		}
		if decoder == nil {
			report.MalformedLines += uint64(len(records))
			checkpoint.RecordsSkipped += uint64(len(records))
			records = records[:0]
			return nil
		}
		decodedFacts, decoded, err := decoder(ctx, conversation, records)
		if err != nil {
			return err
		}
		if len(decoded) != len(records) {
			return fmt.Errorf("conversation driver returned %d decode results for %d records", len(decoded), len(records))
		}
		// A decoded record yields at most one fact, in record order: drivers
		// report records they understood but have nothing to say about (meta
		// or side-chain entries) as decoded without a fact.
		factIndex := 0
		for i, ok := range decoded {
			if !ok {
				report.MalformedLines++
				checkpoint.RecordsSkipped++
				continue
			}
			report.EntriesScanned++
			checkpoint.RecordsImported++
			if factIndex < len(decodedFacts) && decodedFacts[factIndex].Line == records[i].Line {
				facts = append(facts, decodedFacts[factIndex])
				factIndex++
			}
		}
		if factIndex != len(decodedFacts) {
			return fmt.Errorf("conversation driver returned facts for records it did not decode")
		}
		records = records[:0]
		return nil
	}
	commit := func() error {
		if err := flush(); err != nil {
			return err
		}
		if offset == checkpoint.Offset && !changed {
			return nil
		}
		apply.Lock()
		defer apply.Unlock()
		current, currentFound, err := database.SourceCheckpoint(source)
		if err != nil {
			return err
		}
		if currentFound != committedFound || currentFound && (current.Offset != committed.Offset || current.Fingerprint != committed.Fingerprint) {
			return errImportRaced
		}
		index := ConversationIndex{WorkItemID: itemID, ConversationID: conversation.ID, Source: source}
		if !reset {
			if found, err := database.ReadProjection(ConversationProjection, itemID, &index); err != nil {
				return err
			} else if !found {
				return errImportRaced
			}
		}
		attentionSnapshot, err := database.AttentionActivity(itemID)
		if err != nil {
			return err
		}
		attention := attentionSnapshot.Activity
		for _, fact := range facts {
			applyConversationFact(&index, &attention, fact)
		}
		observed := time.Now().UTC()
		checkpoint.Offset = offset
		checkpoint.Size = info.Size()
		checkpoint.ModifiedAt = info.ModTime().UTC()
		checkpoint.ObservedAt = observed
		index.Offset = offset
		index.Size = info.Size()
		index.EntriesScanned = checkpoint.RecordsImported
		index.MalformedLines = checkpoint.RecordsSkipped
		index.ObservedAt = observed
		index.InferredTurnState = inferIndexedConversationTurnState(index)
		attention.ObservedAt = observed
		attention.Source = source
		indexBytes, _ := json.Marshal(index)
		attentionBytes, _ := json.Marshal(attention)
		if _, err := database.CommitSourceBatch(checkpoint, nil, []ProjectionUpdate{{Projection: ConversationProjection, Key: itemID, Value: indexBytes}, {Projection: AttentionActivityProjection, Key: itemID, Value: attentionBytes}}); err != nil {
			return err
		}
		committed, committedFound, reset = checkpoint, true, false
		facts = facts[:0]
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
		} else {
			records = append(records, ConversationRecord{Line: checkpoint.RecordsSeen, Raw: append([]byte(nil), line...)})
		}
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

func applyConversationFact(index *ConversationIndex, attention *AttentionActivity, fact ConversationEventFact) {
	copyFact := fact
	index.LastEvent = &copyFact
	turnActivity := fact.Role == "user" || fact.Role == "assistant" || fact.Role == "toolResult" || fact.Type == "toolResult" || containsCoordinator(fact.ContentTypes, "toolCall", "toolResult", "bashExecution", "tool_use", "tool_result")
	if turnActivity {
		index.LastTurnActivity = &copyFact
	}
	switch {
	case fact.Role == "user" && !fact.Terminal && !containsCoordinator(fact.ContentTypes, "tool_result"):
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
	case fact.Role == "toolResult" || fact.Type == "toolResult" || containsCoordinator(fact.ContentTypes, "toolCall", "toolResult", "bashExecution", "tool_use", "tool_result"):
		index.LastToolActivity = &copyFact
	}
	if fact.Role == "assistant" && containsCoordinator(fact.ContentTypes, "toolCall", "bashExecution", "tool_use") {
		index.LastToolActivity = &copyFact
	}
}

func inferIndexedConversationTurnState(index ConversationIndex) string {
	if index.LastTurnActivity == nil {
		return "idle"
	}
	if index.LastTurnActivity.Terminal && index.LastTurnActivity.Failed {
		return "failed"
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
