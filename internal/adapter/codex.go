package adapter

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"agenthub.local/agenthub/internal/model"
)

type CodexDiscoverer struct {
	Root    string
	Process ProcessState
	Now     func() time.Time
}

func (d CodexDiscoverer) Discover(ctx context.Context) ([]model.Session, error) {
	now := time.Now().UTC()
	if d.Now != nil {
		now = d.Now().UTC()
	}
	// Codex keeps the name a thread shows in its own UI outside the rollout
	// file, in one index for the whole installation, so it is read once per
	// scan rather than per session. A missing or unreadable index is not a
	// failure: it costs the titles, and a row falls back to its ID.
	titles := readCodexThreadNames(d.Root)
	root := filepath.Join(d.Root, "sessions")
	return walkJSONL(root, func(path string, file *os.File, info fs.FileInfo) (model.Session, bool, error) {
		if err := ctx.Err(); err != nil {
			return model.Session{}, false, err
		}
		metadata, ok, err := parseCodexMetadata(file)
		if err != nil {
			return model.Session{}, false, fmt.Errorf("parse Codex metadata %q: %w", path, err)
		}
		if !ok {
			return model.Session{}, false, nil
		}
		return discoveredSession(model.ProviderCodex, metadata.ID, titles[metadata.ID], metadata.CWD, metadata.Source, path, info.ModTime(), d.Process, now), true, nil
	})
}

type codexMetadata struct {
	ID     string
	CWD    string
	Source string
}

// parseCodexMetadata reads an already-open handle so the bytes decoded here
// are the ones the rooted open resolved, not whatever the path names later.
func parseCodexMetadata(file io.Reader) (codexMetadata, bool, error) {
	type record struct {
		Type    string `json:"type"`
		Payload struct {
			ID        string `json:"id"`
			SessionID string `json:"session_id"`
			CWD       string `json:"cwd"`
			Source    any    `json:"source"`
		} `json:"payload"`
	}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for line := 0; line < 16 && scanner.Scan(); line++ {
		var item record
		if err := json.Unmarshal(scanner.Bytes(), &item); err != nil || item.Type != "session_meta" {
			continue
		}
		id := item.Payload.ID
		if id == "" {
			id = item.Payload.SessionID
		}
		metadata := codexMetadata{ID: strings.TrimSpace(id), CWD: item.Payload.CWD, Source: stringifySource(item.Payload.Source)}
		return metadata, validProviderID(metadata.ID), nil
	}
	if err := scanner.Err(); err != nil {
		return codexMetadata{}, false, err
	}
	return codexMetadata{}, false, nil
}

// codexThreadIndex is the file Codex writes a thread's name into. It sits
// beside the sessions directory, not inside it, so it is opened through its
// own rooted handle on the Codex home.
const codexThreadIndex = "session_index.jsonl"

// maxCodexIndexBytes caps how much of the index is read. The file grows by one
// short line per thread; a file far past that is not the index this code
// understands, and reading it whole would be unbounded work at scan time.
const maxCodexIndexBytes = 8 * 1024 * 1024

// readCodexThreadNames maps thread ID to the name Codex shows for it.
//
// Every failure is silent and total-by-row: the index is a convenience, and a
// scan that refused to report sessions because a name file was malformed
// would hide the sessions themselves.
func readCodexThreadNames(home string) map[string]string {
	names := map[string]string{}
	rooted, err := os.OpenRoot(home)
	if err != nil {
		return names
	}
	defer func() { _ = rooted.Close() }()
	file, err := rooted.Open(codexThreadIndex)
	if err != nil {
		return names
	}
	defer func() { _ = file.Close() }()
	if info, err := file.Stat(); err != nil || !info.Mode().IsRegular() {
		return names
	}
	type record struct {
		ID         string `json:"id"`
		ThreadName string `json:"thread_name"`
	}
	scanner := bufio.NewScanner(io.LimitReader(file, maxCodexIndexBytes))
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		var item record
		if err := json.Unmarshal(scanner.Bytes(), &item); err != nil {
			continue
		}
		id := strings.TrimSpace(item.ID)
		if id == "" || item.ThreadName == "" {
			continue
		}
		// Later lines win: the index is appended to when a thread is renamed.
		names[id] = item.ThreadName
	}
	return names
}

func stringifySource(value any) string {
	switch source := value.(type) {
	case string:
		return source
	case map[string]any:
		if len(source) == 1 {
			for key := range source {
				return key
			}
		}
	}
	return "codex"
}

// validProviderID matches what the registry will accept. Keeping the rule in
// one place stops a second ingest path from admitting an ID the export layer
// then has to refuse.
func validProviderID(id string) bool {
	if id == "" || len(id) > 256 || strings.ContainsFunc(id, unicode.IsControl) {
		return false
	}
	return model.ValidateProviderSessionID(id) == nil
}
