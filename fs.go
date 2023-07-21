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

	// OpenWriter opens an io.WriteCloser that represents an instance of a file
	// that can be written to. The parent directory must exist. If the file
	// doesn't exist, it should be created. If the file exists, its should be
	// truncated.
	OpenWriter(name string) (io.WriteCloser, error)

	// Mkdir creates a new directory with the specified name.
	Mkdir(name string) error

	// Remove removes the named file or directory.
	Remove(name string) error

	// Rename renames (moves) oldname to newname. If newname already exists and
	// is not a directory, Rename replaces it.
	Rename(oldname, newname string) error

	// ReadDir reads the named directory and returns a list of directory
	// entries sorted by filename.
	ReadDir(name string) ([]fs.DirEntry, error)
}

type LocalFS struct {
	RootDir string
	TempDir string
}

var _ FS = (*LocalFS)(nil)

func (localFS *LocalFS) Open(name string) (fs.File, error) {
	return os.Open(path.Join(localFS.RootDir, name))
}

func (localFS *LocalFS) OpenWriter(name string) (io.WriteCloser, error) {
	tempDir := localFS.TempDir
	if tempDir == "" {
		tempDir = os.TempDir()
	}
	var err error
	var tempFile *tempFile
	tempFile.source, err = os.CreateTemp(tempDir, "__notebrewtemp*__")
	if err != nil {
		return nil, err
	}
	fileInfo, err := tempFile.source.Stat()
	if err != nil {
		return nil, err
	}
	tempFile.sourcePath = path.Join(tempDir, fileInfo.Name())
	tempFile.destinationPath = path.Join(localFS.RootDir, name)
	return tempFile, nil
}

func (localFS *LocalFS) ReadDir(name string) ([]fs.DirEntry, error) {
	return os.ReadDir(path.Join(localFS.RootDir, name))
}

func (localFS *LocalFS) Mkdir(name string) error {
	return os.Mkdir(path.Join(localFS.RootDir, name), 0755)
}

func (localFS *LocalFS) Remove(name string) error {
	return os.Remove(path.Join(localFS.RootDir, name))
}

func (localFS *LocalFS) Rename(oldname, newname string) error {
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
