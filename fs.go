package nb6

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
)

type FS interface {
	// Open opens the named file.
	//
	// When Open returns an error, it should be of type *PathError
	// with the Op field set to "open", the Path field set to name,
	// and the Err field describing the problem.
	//
	// Open should reject attempts to open names that do not satisfy
	// ValidPath(name), returning a *PathError with Err set to
	// ErrInvalid or ErrNotExist.
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
	// bits (before umask). If there is an error, it will be of type
	// *PathError.
	Mkdir(name string, perm fs.FileMode) error

	// Remove removes the named file or directory. If there is an error, it
	// will be of type *PathError.
	Remove(name string) error

	// Rename renames (moves) oldname to newname. If newname already exists and
	// is not a directory, Rename replaces it. OS-specific restrictions may
	// apply when oldname and newname are in different directories. Even within
	// the same directory, on non-Unix platforms Rename is not an atomic
	// operation. If there is an error, it will be of type *LinkError.
	Rename(oldname, newname string) error
}

var _ = os.DirFS

type dirFS struct {
	dir     string
	tempdir string
}

func DirFS(dir, tempdir string) (dirFS, error) {
	return &dirFS{
	}
}

func (dir dirFS) Open(name string) (fs.File, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	return os.Open(path.Join(string(dir), name))
}

func (dir dirFS) ReadDir(name string) ([]fs.DirEntry, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrInvalid}
	}
	return os.ReadDir(name)
}

func (dir dirFS) OpenWriter(name string) (io.WriteCloser, error) {
	var err error
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "openwriter", Path: name, Err: fs.ErrInvalid}
	}
	tempDir := path.Join(os.TempDir(), "notebrewtempdir")
	err = os.MkdirAll(tempDir, 0755)
	if err != nil {
		return nil, err
	}
	f := &dirFile{
		destFilename: path.Join(string(dir), name),
	}
	f.tempFile, err = os.CreateTemp(tempDir, "*")
	if err != nil {
		return nil, err
	}
	fileInfo, err := f.tempFile.Stat()
	if err != nil {
		return nil, err
	}
	f.tempFilename = path.Join(tempDir, fileInfo.Name())
	return f, nil
}

type dirFile struct {
	// tempFile is temporary file being written to.
	tempFile *os.File

	// tempFilename is the filename of the temporary file being written to.
	tempFilename string

	// destFilename is the destination filename that the temporary file should
	// be renamed to once writing is complete.
	destFilename string

	// writeFailed is true if any calls to tempFile.Write() failed.
	writeFailed bool
}

func (f *dirFile) Write(p []byte) (n int, err error) {
	n, err = f.tempFile.Write(p)
	if err != nil {
		f.writeFailed = true
	}
	return n, err
}

func (f *dirFile) Close() error {
	if f.tempFile == nil {
		return fmt.Errorf("already closed")
	}
	defer func() {
		f.tempFile = nil
		os.Remove(f.tempFilename)
	}()
	err := f.tempFile.Close()
	if err != nil {
		return err
	}
	if f.writeFailed {
		return nil
	}
	return os.Rename(f.tempFilename, f.destFilename)
}
