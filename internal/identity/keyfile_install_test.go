package identity

import (
	"bytes"
	"crypto/ed25519"
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

// A filesystem that cannot link must stop the install, not fall back to a route
// that creates the final name before the content.
//
// The earlier version did fall back, which reopened the very window the
// temporary-file ordering exists to close. Failing here is recoverable — an
// operator moves the data directory — whereas a half-written node.key is not.
func TestLinkFailureLeavesNoKeyBehind(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, keyFileName)

	seed := bytes.Repeat([]byte{0x5A}, ed25519.SeedSize)
	protected, err := protectSeed(seed)
	if err != nil {
		t.Fatal(err)
	}

	refuse := func(string, string) error {
		return &os.LinkError{Op: "link", Err: errors.ErrUnsupported}
	}
	err = installKeyFileLinkedBy(directory, path, protected, refuse)
	if err == nil {
		t.Fatal("a failed link reported a successful install")
	}
	if !errors.Is(err, ErrKeyStorageUnsupported) {
		t.Errorf("error = %v; want ErrKeyStorageUnsupported so the message names the fix", err)
	}

	if _, statErr := os.Stat(path); !errors.Is(statErr, fs.ErrNotExist) {
		t.Errorf("node.key exists after a failed install (stat error = %v); it must never be published partly", statErr)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Errorf("a failed install left %v behind, want an untouched directory", names)
	}
}

// The same failure has to surface through the public entry point, and it must
// not leave a key that a later start would read as this node's identity.
func TestLoadOrCreateReportsAnUnsupportedFilesystem(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, keyFileName)

	protected, err := protectSeed(bytes.Repeat([]byte{0x11}, ed25519.SeedSize))
	if err != nil {
		t.Fatal(err)
	}
	refuse := func(string, string) error {
		return &os.LinkError{Op: "link", Err: errors.ErrUnsupported}
	}
	if err := installKeyFileLinkedBy(directory, path, protected, refuse); !errors.Is(err, ErrKeyStorageUnsupported) {
		t.Fatalf("error = %v; want ErrKeyStorageUnsupported", err)
	}

	// Nothing was installed, so the next start is a first start rather than a
	// start that inherits a broken identity.
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("stat after failed install = %v, want not-exist", err)
	}
	keypair, err := LoadOrCreateKeypair(directory)
	if err != nil {
		t.Fatalf("a working filesystem could not create a key after an earlier failure: %v", err)
	}
	if keypair.Fingerprint() == "" {
		t.Error("no fingerprint")
	}
}

// Shape alone does not prove identity: two observations can each be "a regular
// file" and still be different files. The comparison is unit-tested directly
// because forcing the interleaving in readKeyFile would need brittle timing.
func TestSameFileOrRefuseComparesIdentityNotShape(t *testing.T) {
	directory := t.TempDir()
	first := filepath.Join(directory, "first")
	second := filepath.Join(directory, "second")
	for _, path := range []string{first, second} {
		if err := os.WriteFile(path, []byte("same length"), keyFileMode); err != nil {
			t.Fatal(err)
		}
	}

	firstInfo, err := os.Lstat(first)
	if err != nil {
		t.Fatal(err)
	}
	secondInfo, err := os.Lstat(second)
	if err != nil {
		t.Fatal(err)
	}

	if err := sameFileOrRefuse(firstInfo, firstInfo, first); err != nil {
		t.Errorf("refused one file compared with itself: %v", err)
	}
	// Same mode, same size, same directory — only identity separates them.
	if err := sameFileOrRefuse(firstInfo, secondInfo, first); err == nil {
		t.Error("accepted two different files as one; the shape checks cannot tell them apart")
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
