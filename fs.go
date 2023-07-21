package nb6

import (
	"io"
	"io/fs"
	"os"
	"path"
)

type FS interface {
	// Open opens the named file.
	Open(name string) (fs.File, error)

	// ReadDir reads the named directory and returns a list of directory
	// entries sorted by filename.
	ReadDir(name string) ([]fs.DirEntry, error)

	// OpenWriter opens an io.WriteCloser that represents an instance of a file
	// that can be written to. The parent directory must exist. If the file
	// doesn't exist, it should be created. If the file exists, its should be
	// truncated.
	OpenWriter(name string, perm fs.FileMode) (io.WriteCloser, error)

	// Mkdir creates a new directory with the specified name and permission
	// bits.
	Mkdir(name string, perm fs.FileMode) error

	// Remove removes the named file or directory.
	Remove(name string) error

	// Rename renames (moves) oldname to newname. If newname already exists and
	// is not a directory, Rename replaces it.
	Rename(oldname, newname string) error
}

type LocalFS struct {
	RootDir string
	TempDir string
}

var _ FS = (*LocalFS)(nil)

func (localFS *LocalFS) Open(name string) (fs.File, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	return os.Open(path.Join(localFS.RootDir, name))
}

func (localFS *LocalFS) ReadDir(name string) ([]fs.DirEntry, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrInvalid}
	}
	return os.ReadDir(path.Join(localFS.RootDir, name))
}

func (localFS *LocalFS) OpenWriter(name string, perm fs.FileMode) (io.WriteCloser, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "openwriter", Path: name, Err: fs.ErrInvalid}
	}
	var err error
	var tempFile *tempFile
	tempFile.source, err = os.CreateTemp(localFS.TempDir, "*")
	if err != nil {
		return nil, err
	}
	fileInfo, err := tempFile.source.Stat()
	if err != nil {
		return nil, err
	}
	tempFile.sourcePath = path.Join(localFS.TempDir, fileInfo.Name())
	tempFile.destinationPath = path.Join(localFS.RootDir, name)
	err = os.Chmod(tempFile.sourcePath, perm)
	if err != nil {
		tempFile.Close()
		return nil, err
	}
	return tempFile, nil
}

func (localFS *LocalFS) Mkdir(name string, perm fs.FileMode) error {
	if !fs.ValidPath(name) {
		return &fs.PathError{Op: "mkdir", Path: name, Err: fs.ErrInvalid}
	}
	return os.Mkdir(path.Join(localFS.RootDir, name), perm)
}

func (localFS *LocalFS) Remove(name string) error {
	if !fs.ValidPath(name) {
		return &fs.PathError{Op: "remove", Path: name, Err: fs.ErrInvalid}
	}
	return os.Remove(path.Join(localFS.RootDir, name))
}

func (localFS *LocalFS) Rename(oldname, newname string) error {
	if !fs.ValidPath(oldname) {
		return &fs.PathError{Op: "rename", Path: oldname, Err: fs.ErrInvalid}
	}
	if !fs.ValidPath(newname) {
		return &fs.PathError{Op: "rename", Path: newname, Err: fs.ErrInvalid}
	}
	return os.Rename(path.Join(localFS.RootDir, oldname), path.Join(localFS.RootDir, newname))
}

type tempFile struct {
	// source is temporary file being written to.
	source *os.File

	// sourcePath is the path of the temporary file being written to.
	sourcePath string

	// destinationPath is the path that the temporary file should be renamed to
	// once writing is complete.
	destinationPath string

	// writeFailed is true if any calls to Write() failed.
	writeFailed bool
}

func (tempFile *tempFile) Write(p []byte) (n int, err error) {
	n, err = tempFile.source.Write(p)
	if err != nil {
		tempFile.writeFailed = true
	}
	return n, err
}

func (tempFile *tempFile) Close() error {
	if tempFile.source == nil {
		return fs.ErrClosed
	}
	defer func() {
		tempFile.source = nil
		os.Remove(tempFile.sourcePath)
	}()
	err := tempFile.source.Close()
	if err != nil {
		return err
	}
	if tempFile.writeFailed {
		return nil
	}
	return os.Rename(tempFile.sourcePath, tempFile.destinationPath)
}
