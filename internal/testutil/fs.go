package testutil

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"sync"
	"testing/fstest"
	"time"
)

type FS interface {
	Open(name string) (fs.File, error)
	OpenWriter(name string, perm fs.FileMode) (io.WriteCloser, error)
	Mkdir(name string, perm fs.FileMode) error
	Remove(name string) error
	Rename(oldname, newname string) error
	ReadDir(name string) ([]fs.DirEntry, error)
}

type TestFS struct {
	mu    sync.RWMutex
	mapFS fstest.MapFS
}

func NewFS(mapFS fstest.MapFS) *TestFS {
	if mapFS == nil {
		mapFS = make(fstest.MapFS)
	}
	return &TestFS{mapFS: mapFS}
}

func (testFS *TestFS) Open(name string) (fs.File, error) {
	testFS.mu.RLock()
	defer testFS.mu.RUnlock()
	return testFS.mapFS.Open(name)
}

func (testFS *TestFS) OpenWriter(name string, perm fs.FileMode) (io.WriteCloser, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "openwriter", Path: name, Err: fs.ErrInvalid}
	}
	testFile := &testFile{
		testFS: testFS,
		name:   name,
		buffer: &bytes.Buffer{},
		mode:   perm,
	}
	return testFile, nil
}

func (testFS *TestFS) Mkdir(name string, perm fs.FileMode) error {
	if !fs.ValidPath(name) {
		return &fs.PathError{Op: "mkdir", Path: name, Err: fs.ErrInvalid}
	}
	_, err := fs.Stat(testFS, path.Dir(name))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("parent directory does not exist")
		}
		return err
	}
	testFS.mapFS[name] = &fstest.MapFile{
		Mode:    perm | fs.ModeDir,
		ModTime: time.Now(),
	}
	return nil
}

type testFile struct {
	testFS *TestFS
	name   string
	buffer *bytes.Buffer
	mode   fs.FileMode
}

func (testFile *testFile) Write(p []byte) (n int, err error) {
	return testFile.buffer.Write(p)
}

func (testFile *testFile) Close() error {
	if testFile.buffer == nil {
		return fs.ErrClosed
	}
	defer func() {
		testFile.buffer = nil
	}()
	fileInfo, err := fs.Stat(testFile.testFS, testFile.name)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if fileInfo != nil && fileInfo.IsDir() {
		return fmt.Errorf("directory named %q already exists", testFile.name)
	}
	testFile.testFS.mu.Lock()
	defer testFile.testFS.mu.Unlock()
	testFile.testFS.mapFS[testFile.name] = &fstest.MapFile{
		Data:    testFile.buffer.Bytes(),
		ModTime: time.Now(),
		Mode:    testFile.mode &^ fs.ModeDir,
	}
	return nil
}
