package nb6

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
)

type FS interface {
	// Open opens the named file.
	Open(name string) (fs.File, error)

	// OpenReaderFrom opens an io.ReaderFrom that represents an instance of a
	// file that can read from an io.Reader. The parent directory must exist.
	// If the file doesn't exist, it should be created. If the file exists, its
	// should be truncated.
	OpenReaderFrom(name string, perm fs.FileMode) (io.ReaderFrom, error)

	// ReadDir reads the named directory and returns a list of directory
	// entries sorted by filename.
	ReadDir(name string) ([]fs.DirEntry, error)

	// Mkdir creates a new directory with the specified name.
	Mkdir(name string, perm fs.FileMode) error

	// Remove removes the named file or directory.
	Remove(name string) error

	// Rename renames (moves) oldname to newname. If newname already exists and
	// is not a directory, Rename replaces it.
	Rename(oldname, newname string) error
}

// OpenReaderFrom(name string, perm fs.FileMode) (io.ReaderFrom, error)

type LocalFS struct {
	RootDir string
	TempDir string
}

var _ FS = (*LocalFS)(nil)

func (localFS *LocalFS) Open(name string) (fs.File, error) {
	return os.Open(path.Join(localFS.RootDir, name))
}

func (localFS *LocalFS) OpenReaderFrom(name string, perm fs.FileMode) (io.ReaderFrom, error) {
	return &localFile{
		localFS: localFS,
		name:    name,
		perm:    perm,
	}, nil
}

func (localFS *LocalFS) ReadDir(name string) ([]fs.DirEntry, error) {
	return os.ReadDir(path.Join(localFS.RootDir, name))
}

func (localFS *LocalFS) Mkdir(name string, perm fs.FileMode) error {
	return os.Mkdir(path.Join(localFS.RootDir, name), perm)
}

func (localFS *LocalFS) MkdirAll(name string, perm fs.FileMode) error {
	return os.MkdirAll(path.Join(localFS.RootDir, name), perm)
}

func (localFS *LocalFS) Remove(name string) error {
	return os.Remove(path.Join(localFS.RootDir, name))
}

func (localFS *LocalFS) RemoveAll(name string) error {
	return os.RemoveAll(path.Join(localFS.RootDir, name))
}

func (localFS *LocalFS) Rename(oldname, newname string) error {
	return os.Rename(path.Join(localFS.RootDir, oldname), path.Join(localFS.RootDir, newname))
}

type localFile struct {
	localFS *LocalFS
	name    string
	perm    fs.FileMode
}

func (localFile *localFile) ReadFrom(r io.Reader) (n int64, err error) {
	tempDir := localFile.localFS.TempDir
	if tempDir == "" {
		tempDir = os.TempDir()
	}
	tempFile, err := os.CreateTemp(tempDir, "__notebrewtemp*__")
	if err != nil {
		return 0, err
	}
	defer tempFile.Close()
	tempFileInfo, err := tempFile.Stat()
	if err != nil {
		return 0, err
	}
	tempFileName := path.Join(tempDir, tempFileInfo.Name())
	defer os.Remove(tempFileName)
	n, err = io.Copy(tempFile, r)
	if err != nil {
		return 0, err
	}
	err = tempFile.Close()
	if err != nil {
		return 0, err
	}
	destFileName := path.Join(localFile.localFS.RootDir, localFile.name)
	destFileInfo, err := os.Stat(destFileName)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return 0, err
	}
	mode := localFile.perm
	if destFileInfo != nil {
		mode = destFileInfo.Mode()
	}
	err = os.Rename(tempFileName, localFile.name)
	if err != nil {
		return 0, err
	}
	_ = os.Chmod(destFileName, mode)
	return n, nil
}

// func mkdirAll()

// func removeAll()

// func move()
