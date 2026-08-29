package atomicfile

import (
	"io/fs"
	"os"
	"path/filepath"

	"github.com/m-mizutani/goerr/v2"
)

// Write replaces path with data by writing a temporary file in the same
// directory and renaming it. An interrupted write therefore never leaves a
// half-written file where the previous contents used to be.
//
// dirMode is used only when the parent directory has to be created.
func Write(path string, data []byte, mode, dirMode fs.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return goerr.Wrap(err, "failed to create directory", goerr.V("dir", dir))
	}

	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return goerr.Wrap(err, "failed to create temporary file", goerr.V("dir", dir))
	}
	tmpName := tmp.Name()

	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}

	// Set the permission before writing so the contents are never briefly
	// readable under the default umask.
	if err := tmp.Chmod(mode); err != nil {
		cleanup()
		return goerr.Wrap(err, "failed to set permission", goerr.V("path", tmpName))
	}
	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return goerr.Wrap(err, "failed to write temporary file", goerr.V("path", tmpName))
	}
	// Flush the contents before the rename. Without this, a crash shortly after
	// the rename can leave the new name pointing at a zero-length file, which is
	// worse than not having written at all.
	if err := tmp.Sync(); err != nil {
		cleanup()
		return goerr.Wrap(err, "failed to flush temporary file", goerr.V("path", tmpName))
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return goerr.Wrap(err, "failed to close temporary file", goerr.V("path", tmpName))
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return goerr.Wrap(err, "failed to replace file", goerr.V("path", path))
	}
	syncDir(dir)
	return nil
}

// syncDir flushes the directory entry so the rename itself survives a crash.
// Failures are ignored: not every platform or filesystem supports it, and the
// data is already written either way.
func syncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	defer func() { _ = d.Close() }()
	_ = d.Sync()
}
