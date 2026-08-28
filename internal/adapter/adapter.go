package adapter

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
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

// walkJSONL visits every regular .jsonl file under root through a rooted
// handle. os.Root refuses names that leave the root through "..", an absolute
// path, or a symlink, and it resolves the name at open time, so a directory
// entry swapped for a link between the walk and the read cannot redirect the
// read either. Symlinked entries are skipped outright: a provider writes its
// metadata as plain files, and following a link is how a writer who only
// controls the root turns AgentHub into a reader of somebody else's file.
//
// Only entries the walk itself rules out are skipped silently. Once an entry
// looks like provider metadata, a refused open or a failed Stat aborts the
// whole discovery run: an escape the root rejected, a permission denial, an
// I/O error or an exhausted descriptor table all mean the registry this run
// would return is not the registry on disk, and reporting that as a smaller
// but successful result is how an attack or an operational fault stays
// invisible.
func walkJSONL(root string, visit func(string, *os.File, fs.FileInfo) (model.Session, bool, error)) ([]model.Session, error) {
	rooted, err := os.OpenRoot(root)
	if errors.Is(err, fs.ErrNotExist) {
		return []model.Session{}, nil
	} else if err != nil {
		return nil, err
	}
	defer func() { _ = rooted.Close() }()

	byID := make(map[string]model.Session)
	err = fs.WalkDir(rooted.FS(), ".", func(relativePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || entry.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(entry.Name()), ".jsonl") {
			return nil
		}
		path := filepath.Join(root, filepath.FromSlash(relativePath))
		if !entry.Type().IsRegular() {
			// The directory already told us this is a FIFO, a device or a
			// socket. None of those is provider metadata, and opening one
			// can block the run forever, so it is dropped before the open.
			return nil
		}
		file, err := rooted.Open(relativePath)
		if err != nil {
			return fmt.Errorf("open session metadata %q: %w", path, err)
		}
		defer func() { _ = file.Close() }()
		info, err := file.Stat()
		if err != nil {
			return fmt.Errorf("stat session metadata %q: %w", path, err)
		}
		if !info.Mode().IsRegular() {
			// The entry changed type between the walk and the open. Nothing
			// is read from it, but the run still stops: a metadata path that
			// turned into a pipe under us is not a benign scan result.
			return fmt.Errorf("session metadata %q is not a regular file: %v", path, info.Mode().Type())
		}
		session, ok, err := visit(path, file, info)
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
