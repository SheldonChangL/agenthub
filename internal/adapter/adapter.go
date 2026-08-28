package adapter

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"sort"
	"strings"
	"time"

	"agenthub.local/agenthub/internal/model"
	statusmodel "agenthub.local/agenthub/internal/status"
)

type Discoverer interface {
	Discover(context.Context) ([]model.Session, error)
}

type ProcessState struct {
	Known   bool
	Running bool
}

func walkJSONL(root string, visit func(string, fs.FileInfo) (model.Session, bool, error)) ([]model.Session, error) {
	if _, err := os.Stat(root); errors.Is(err, os.ErrNotExist) {
		return []model.Session{}, nil
	} else if err != nil {
		return nil, err
	}

	byID := make(map[string]model.Session)
	err := fs.WalkDir(os.DirFS(root), ".", func(relativePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".jsonl") {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		path := root + string(os.PathSeparator) + relativePath
		session, ok, err := visit(path, info)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		if existing, found := byID[session.ID]; !found || session.LastSeenAt.After(existing.LastSeenAt) {
			byID[session.ID] = session
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	sessions := make([]model.Session, 0, len(byID))
	for _, session := range byID {
		sessions = append(sessions, session)
	}
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].ID < sessions[j].ID })
	return sessions, nil
}

func discoveredSession(provider model.Provider, providerID, cwd, source, metadataPath string, modifiedAt time.Time, process ProcessState, now time.Time) model.Session {
	status, statusSource := statusmodel.Infer(statusmodel.Evidence{
		Management:     model.Unmanaged,
		MetadataAt:     modifiedAt,
		ProcessKnown:   process.Known,
		ProcessRunning: process.Running,
	}, statusmodel.Policy{Now: now})
	return model.Session{
		ID:                model.SessionID(provider, providerID),
		Provider:          provider,
		ProviderSessionID: providerID,
		Management:        model.Unmanaged,
		Visibility:        model.VisibilityPrivate,
		Status:            status,
		StatusSource:      statusSource,
		CWD:               cwd,
		Source:            source,
		MetadataPath:      metadataPath,
		LastSeenAt:        modifiedAt.UTC(),
		UpdatedAt:         now.UTC(),
	}
}
