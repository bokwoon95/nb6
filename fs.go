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

	// OpenWriter opens an io.WriteCloser that represents an instance of a file
	// that can be written to. The parent directory must exist. If the file
	// doesn't exist, it should be created. If the file exists, its should be
	// truncated.
	OpenWriter(name string, perm fs.FileMode) (io.WriteCloser, error)

	// TODO: Remove OpenWriter(), replace with OpenReaderFrom() instead.
	// OpenReaderFrom(name string, perm fs.FileMode) (io.ReaderFrom, error)

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

func (localFS *LocalFS) OpenWriter(name string, perm fs.FileMode) (io.WriteCloser, error) {
	tempDir := localFS.TempDir
	if tempDir == "" {
		tempDir = os.TempDir()
	}
	file, err := os.CreateTemp(tempDir, "__notebrewtemp*__")
	if err != nil {
		return nil, err
	}
	tempFile := &tempFile{
		rootDir:  localFS.RootDir,
		tempDir:  tempDir,
		file:     file,
		destName: name,
		perm:     perm,
	}
	return tempFile, nil
}

func (localFS *LocalFS) OpenReaderFrom(name string, perm fs.FileMode) (io.ReaderFrom, error) {
	return &fileWriter{
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

type tempFile struct {
	// Root directory.
	rootDir string

	// Temp directory.
	tempDir string

	// file is temporary file being written to.
	file *os.File

	// destName is the name passed to OpenWriter().
	destName string

	// perm is the permission passed to OpenWriter().
	perm fs.FileMode

	// writeFailed is true if any calls to Write() failed.
	writeFailed bool
}

func (tempFile *tempFile) Write(p []byte) (n int, err error) {
	n, err = tempFile.file.Write(p)
	if err != nil {
		tempFile.writeFailed = true
	}
	return n, err
}

func (tempFile *tempFile) Close() error {
	if tempFile.file == nil {
		return fs.ErrClosed
	}
	srcFileInfo, err := tempFile.file.Stat()
	if err != nil {
		return err
	}
	srcPath := path.Join(tempFile.tempDir, srcFileInfo.Name())
	defer func() {
		tempFile.file.Close()
		tempFile.file = nil
		os.Remove(srcPath)
	}()
	if tempFile.writeFailed {
		return nil
	}
	destPath := path.Join(tempFile.rootDir, tempFile.destName)
	destFileInfo, err := os.Stat(destPath)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	err = tempFile.file.Close()
	if err != nil {
		return err
	}
	err = os.Rename(srcPath, destPath)
	if err != nil {
		return err
	}
	mode := tempFile.perm
	if destFileInfo != nil {
		mode = destFileInfo.Mode()
	}
	return os.Chmod(destPath, mode)
}

type fileWriter struct {
	localFS *LocalFS
	name    string
	perm    fs.FileMode
}

func (fw *fileWriter) ReadFrom(r io.Reader) (n int64, err error) {
	tempDir := fw.localFS.TempDir
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
	err = os.Rename(tempFileName, fw.name)
	if err != nil {
		return 0, err
	}
	_ = os.Chmod(fw.name, fw.perm)
	return n, nil
}

// func mkdirAll()

// func removeAll()

// func move()
