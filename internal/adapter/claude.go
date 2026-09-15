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
	return walkJSONL(root, func(path string, file *os.File, info fs.FileInfo) (model.Session, bool, error) {
		if err := ctx.Err(); err != nil {
			return model.Session{}, false, err
		}
		metadata, ok, err := parseClaudeMetadata(file)
		if err != nil {
			return model.Session{}, false, fmt.Errorf("parse Claude metadata %q: %w", path, err)
		}
		if !ok || metadata.IsSidechain {
			return model.Session{}, false, nil
		}
		if title, err := latestClaudeTitle(file, info.Size()); err != nil {
			return model.Session{}, false, fmt.Errorf("read Claude title %q: %w", path, err)
		} else if title != "" {
			metadata.Title = title
		}
		return discoveredSession(model.ProviderClaude, metadata.SessionID, metadata.Title, metadata.CWD, "claude-code", path, info.ModTime(), d.Process, now), true, nil
	})
}

type claudeMetadata struct {
	SessionID   string
	Title       string
	CWD         string
	IsSidechain bool
}

// claudeTitleWindow is how much of the end of a transcript is read to find the
// session's current name.
//
// Measured against 1099 real transcripts: 32KB recovers 152 titles for 35MB of
// reads, and 256KB recovers 153 for 208MB. The eightfold read buys one title,
// so the window is small and the head scan covers what it misses.
const claudeTitleWindow = 32 * 1024

// parseClaudeMetadata reads an already-open handle so the bytes decoded here
// are the ones the rooted open resolved, not whatever the path names later.
func parseClaudeMetadata(file io.Reader) (claudeMetadata, bool, error) {
	type record struct {
		Type        string `json:"type"`
		SessionID   string `json:"sessionId"`
		CustomTitle string `json:"customTitle"`
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
		// Last one wins, here and in the tail scan: a rename appends a new
		// line rather than editing the old one, so the first title in the
		// file is the name the session used to have.
		if item.Type == claudeTitleRecord && item.CustomTitle != "" {
			metadata.Title = item.CustomTitle
		}
		metadata.IsSidechain = metadata.IsSidechain || item.IsSidechain
		// A sidechain is never listed, so nothing after this line is worth
		// reading. Most files under the projects directory are sidechains,
		// and dropping this short-circuit doubled the cost of a full scan.
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

const claudeTitleRecord = "custom-title"

// latestClaudeTitle reads the end of a transcript for the name the session
// carries now. An empty result means the window held no title line, which
// leaves the caller with whatever the head scan found.
func latestClaudeTitle(file io.ReaderAt, size int64) (string, error) {
	lines, err := tailLines(file, size, claudeTitleWindow)
	if err != nil {
		return "", err
	}
	type record struct {
		Type        string `json:"type"`
		CustomTitle string `json:"customTitle"`
	}
	title := ""
	for _, line := range lines {
		var item record
		if err := json.Unmarshal(line, &item); err != nil {
			continue
		}
		if item.Type == claudeTitleRecord && item.CustomTitle != "" {
			title = item.CustomTitle
		}
	}
	return title, nil
}
