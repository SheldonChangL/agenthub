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

	"agenthub.local/agenthub/internal/model"
)

type ClaudeDiscoverer struct {
	Root    string
	Process ProcessState
	Now     func() time.Time
}

func (d ClaudeDiscoverer) Discover(ctx context.Context) ([]model.Session, error) {
	now := time.Now().UTC()
	if d.Now != nil {
		now = d.Now().UTC()
	}
	root := filepath.Join(d.Root, "projects")
	return walkJSONL(root, func(path string, info fs.FileInfo) (model.Session, bool, error) {
		if err := ctx.Err(); err != nil {
			return model.Session{}, false, err
		}
		metadata, ok, err := parseClaudeMetadata(path)
		if err != nil {
			return model.Session{}, false, fmt.Errorf("parse Claude metadata %q: %w", path, err)
		}
		if !ok || metadata.IsSidechain {
			return model.Session{}, false, nil
		}
		return discoveredSession(model.ProviderClaude, metadata.SessionID, metadata.CWD, "claude-code", path, info.ModTime(), d.Process, now), true, nil
	})
}

type claudeMetadata struct {
	SessionID   string
	CWD         string
	IsSidechain bool
}

func parseClaudeMetadata(path string) (claudeMetadata, bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return claudeMetadata{}, false, err
	}
	defer file.Close()

	type record struct {
		SessionID   string `json:"sessionId"`
		CWD         string `json:"cwd"`
		IsSidechain bool   `json:"isSidechain"`
	}
	var metadata claudeMetadata
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for line := 0; line < 256 && scanner.Scan(); line++ {
		var item record
		if err := json.Unmarshal(scanner.Bytes(), &item); err != nil {
			continue
		}
		if metadata.SessionID == "" {
			metadata.SessionID = item.SessionID
		}
		if metadata.CWD == "" {
			metadata.CWD = item.CWD
		}
		metadata.IsSidechain = metadata.IsSidechain || item.IsSidechain
		if metadata.SessionID != "" && metadata.CWD != "" && metadata.IsSidechain {
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return claudeMetadata{}, false, err
	}
	metadata.SessionID = strings.TrimSpace(metadata.SessionID)
	return metadata, validProviderID(metadata.SessionID), nil
}
