package identity

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// Every start that races on a fresh directory must end up on one identity.
//
// A node whose key depends on who won a startup race has no stable identity:
// half the peers would have paired with a fingerprint that no longer exists.
func TestConcurrentStartsAgreeOnOneKey(t *testing.T) {
	directory := t.TempDir()

	const starts = 16
	fingerprints := make([]string, starts)
	errs := make([]error, starts)
	var ready sync.WaitGroup
	var done sync.WaitGroup
	ready.Add(starts)
	done.Add(starts)
	release := make(chan struct{})

	for i := range starts {
		go func() {
			defer done.Done()
			ready.Done()
			<-release
			keypair, err := LoadOrCreateKeypair(directory)
			errs[i] = err
			if err == nil {
				fingerprints[i] = keypair.Fingerprint()
			}
		}()
	}
	ready.Wait()
	close(release)
	done.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("start %d failed: %v", i, err)
		}
	}
	for i, fingerprint := range fingerprints {
		if fingerprint != fingerprints[0] {
			t.Fatalf("start %d has fingerprint %q, start 0 has %q", i, fingerprint, fingerprints[0])
		}
	}

	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != keyFileName {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Errorf("directory holds %v, want only %s", names, keyFileName)
	}
}

// The regression this pins: the final name must never exist while empty.
//
// An earlier version claimed node.key with O_EXCL and only then wrote the seed
// to a temporary file, so a crash — or any failure — in between left a 0-byte
// node.key that every later start refuses, and only a human deleting the file
// could recover the node. A watcher that stats the path throughout creation
// would have caught that; it must never see an empty key now.
func TestCreationNeverPublishesAnEmptyKey(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, keyFileName)

	var sawEmpty atomic.Bool
	var samples atomic.Int64
	stop := make(chan struct{})
	var watching sync.WaitGroup
	watching.Add(1)
	go func() {
		defer watching.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if info, err := os.Stat(path); err == nil {
				samples.Add(1)
				if info.Size() == 0 {
					sawEmpty.Store(true)
				}
			}
		}
	}()

	keypair, err := LoadOrCreateKeypair(directory)
	close(stop)
	watching.Wait()
	if err != nil {
		t.Fatal(err)
	}

	if sawEmpty.Load() {
		t.Error("node.key existed while empty; a failure in that window strands a key every later start refuses")
	}
	if samples.Load() == 0 {
		t.Log("the watcher never observed the file; the assertion above did not exercise the window")
	}
	if keypair.Fingerprint() == "" {
		t.Error("no fingerprint")
	}
}

// The fallback used when a filesystem has no hard links must produce the same
// key file, and must still refuse to overwrite an identity that already exists.
func TestExclusiveCreateFallbackInstallsACompleteKey(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, keyFileName)

	seed := bytes.Repeat([]byte{0x5A}, 32)
	protected, err := protectSeed(seed)
	if err != nil {
		t.Fatal(err)
	}
	if err := createKeyFileExclusively(path, protected, errors.New("pretend links are unsupported")); err != nil {
		t.Fatal(err)
	}

	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stored, protected) {
		t.Error("the fallback wrote something other than the protected key")
	}
	keypair, err := LoadOrCreateKeypair(directory)
	if err != nil {
		t.Fatalf("the fallback wrote a key the loader rejects: %v", err)
	}
	if keypair.Fingerprint() == "" {
		t.Error("no fingerprint")
	}

	// A second attempt is a lost race, reported as ErrExist so the caller reads
	// the winner rather than replacing it.
	err = createKeyFileExclusively(path, protected, errors.New("pretend links are unsupported"))
	if !errors.Is(err, fs.ErrExist) {
		t.Errorf("second create returned %v, want fs.ErrExist", err)
	}
	again, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stored, again) {
		t.Error("a losing create modified the installed key")
	}
}

// A write that cannot complete has to be reported, not swallowed: the caller
// removes the file it created only because this returns an error.
func TestWriteAndSyncReportsAFailedWrite(t *testing.T) {
	file, err := os.Create(filepath.Join(t.TempDir(), "closed"))
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := writeAndSync(file, bytes.Repeat([]byte{1}, 32)); err == nil {
		t.Error("writeAndSync reported success on a closed file")
	}
}

// A key that is already on disk is the node's identity. A second start reads it
// and must not replace it, even byte for byte.
func TestExistingKeyIsNeverRewritten(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, keyFileName)

	first, err := LoadOrCreateKeypair(directory)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	second, err := LoadOrCreateKeypair(directory)
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Error("the stored key changed on a load that should only have read it")
	}
	if first.Fingerprint() != second.Fingerprint() {
		t.Error("the second start produced a different identity")
	}
}

// A corrupt key is reported and left alone. Replacing it would swap the node's
// identity for a new one and silently break every pairing; the owner has to see
// the file and decide.
func TestCorruptKeyIsNotReplaced(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, keyFileName)
	corrupt := []byte("not a node key")
	if err := os.WriteFile(path, corrupt, keyFileMode); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadOrCreateKeypair(directory); err == nil {
		t.Fatal("a corrupt key file was accepted")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(corrupt, after) {
		t.Error("the corrupt key was overwritten; a regenerated identity invalidates every pairing")
	}
}

// Anything other than a plain file under node.key means something else decided
// where this node's identity comes from.
func TestLoadRefusesSomethingOtherThanARegularFile(t *testing.T) {
	t.Run("symlink", func(t *testing.T) {
		directory := t.TempDir()
		elsewhere := filepath.Join(t.TempDir(), "borrowed.key")
		if err := os.WriteFile(elsewhere, bytes.Repeat([]byte{7}, 32), keyFileMode); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(elsewhere, filepath.Join(directory, keyFileName)); err != nil {
			t.Skipf("this platform cannot create a symlink here: %v", err)
		}
		_, err := LoadOrCreateKeypair(directory)
		if err == nil {
			t.Fatal("a symlinked node key was followed")
		}
		if !strings.Contains(err.Error(), "regular file") {
			t.Errorf("error = %v; want it to name the file shape it refused", err)
		}
	})

	t.Run("directory", func(t *testing.T) {
		directory := t.TempDir()
		if err := os.Mkdir(filepath.Join(directory, keyFileName), 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadOrCreateKeypair(directory); err == nil {
			t.Error("a directory named node.key was accepted")
		}
	})
}

// A key file is at most a few hundred bytes. A huge one is not a key, and
// reading it whole is how an unbounded file becomes an unbounded allocation.
func TestLoadRefusesAnAbsurdlyLargeKeyFile(t *testing.T) {
	directory := t.TempDir()
	oversized := bytes.Repeat([]byte{0}, maxKeyFileSize+1)
	if err := os.WriteFile(filepath.Join(directory, keyFileName), oversized, keyFileMode); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateKeypair(directory); err == nil {
		t.Error("an oversized node key file was read")
	}
}
