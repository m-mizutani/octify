package atomicfile_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/m-mizutani/gt"
	"github.com/m-mizutani/octify/pkg/utils/atomicfile"
)

func TestWriteCreatesFileAndDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "created")
	path := filepath.Join(dir, "data.json")

	gt.NoError(t, atomicfile.Write(path, []byte("hello"), 0o600, 0o700))

	content := gt.R1(os.ReadFile(path)).NoError(t)
	gt.Equal(t, string(content), "hello")

	fileInfo := gt.R1(os.Stat(path)).NoError(t)
	gt.Equal(t, fileInfo.Mode().Perm(), os.FileMode(0o600))

	dirInfo := gt.R1(os.Stat(dir)).NoError(t)
	gt.Equal(t, dirInfo.Mode().Perm(), os.FileMode(0o700))
}

func TestWriteReplacesExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data.json")

	gt.NoError(t, atomicfile.Write(path, []byte("first"), 0o600, 0o700))
	gt.NoError(t, atomicfile.Write(path, []byte("second"), 0o600, 0o700))

	content := gt.R1(os.ReadFile(path)).NoError(t)
	gt.Equal(t, string(content), "second")

	// The temporary file must not survive the rename.
	entries := gt.R1(os.ReadDir(dir)).NoError(t)
	gt.A(t, entries).Length(1)
	gt.Equal(t, entries[0].Name(), "data.json")
}

func TestWriteFailsOnUnwritableDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("a read-only directory does not stop writes when running as root")
	}

	dir := filepath.Join(t.TempDir(), "read-only")
	gt.NoError(t, os.Mkdir(dir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	gt.Error(t, atomicfile.Write(filepath.Join(dir, "data.json"), []byte("x"), 0o600, 0o700))
}

func TestWriteLeavesPreviousContentWhenTargetIsADirectory(t *testing.T) {
	dir := t.TempDir()
	// Renaming onto a directory fails, which exercises the cleanup path.
	target := filepath.Join(dir, "occupied")
	gt.NoError(t, os.Mkdir(target, 0o700))

	gt.Error(t, atomicfile.Write(target, []byte("x"), 0o600, 0o700))

	entries := gt.R1(os.ReadDir(dir)).NoError(t)
	gt.A(t, entries).Length(1)
	gt.Equal(t, entries[0].Name(), "occupied")
}
