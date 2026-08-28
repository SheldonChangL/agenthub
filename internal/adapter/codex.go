package adapter

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
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
	root := filepath.Join(d.Root, "sessions")
	return walkJSONL(root, func(path string, info fs.FileInfo) (model.Session, bool, error) {
		if err := ctx.Err(); err != nil {
			return model.Session{}, false, err
		}
		metadata, ok, err := parseCodexMetadata(path)
		if err != nil {
			return model.Session{}, false, fmt.Errorf("parse Codex metadata %q: %w", path, err)
		}
		if !ok {
			return model.Session{}, false, nil
		}
		return discoveredSession(model.ProviderCodex, metadata.ID, metadata.CWD, metadata.Source, path, info.ModTime(), d.Process, now), true, nil
	})
}

type codexMetadata struct {
	ID     string
	CWD    string
	Source string
}

func parseCodexMetadata(path string) (codexMetadata, bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return codexMetadata{}, false, err
	}
	defer file.Close()

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
