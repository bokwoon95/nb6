package testutil

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"strings"
	"sync"
	"testing/fstest"
	"time"
)

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

func (testFS *TestFS) ReadDir(name string) ([]fs.DirEntry, error) {
	testFS.mu.RLock()
	defer testFS.mu.RUnlock()
	return testFS.mapFS.ReadDir(name)
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
	testFS.mu.Lock()
	defer testFS.mu.Unlock()
	testFS.mapFS[name] = &fstest.MapFile{
		Mode:    perm | fs.ModeDir,
		ModTime: time.Now(),
	}
	return nil
}

func (testFS *TestFS) Remove(name string) error {
	if !fs.ValidPath(name) {
		return &fs.PathError{Op: "remove", Path: name, Err: fs.ErrInvalid}
	}
	fileInfo, err := fs.Stat(testFS, name)
	if err != nil {
		return err
	}
	if fileInfo.IsDir() {
		dirEntries, err := testFS.ReadDir(name)
		if err != nil {
			return err
		}
		if len(dirEntries) > 0 {
			return fmt.Errorf("directory not empty")
		}
	}
	testFS.mu.Lock()
	defer testFS.mu.Unlock()
	delete(testFS.mapFS, name)
	return nil
}

func (testFS *TestFS) Rename(oldname, newname string) error {
	if !fs.ValidPath(oldname) {
		return &fs.PathError{Op: "rename", Path: oldname, Err: fs.ErrInvalid}
	}
	if !fs.ValidPath(newname) {
		return &fs.PathError{Op: "rename", Path: newname, Err: fs.ErrInvalid}
	}
	oldFileInfo, err := fs.Stat(testFS, oldname)
	if err != nil {
		return err
	}
	newFileInfo, err := fs.Stat(testFS, newname)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if newFileInfo != nil && newFileInfo.IsDir() {
		return fmt.Errorf("cannot rename to %[1]q to %[2]q: %[2]q already exists and is a directory", oldname, newname)
	}
	testFS.mu.Lock()
	defer testFS.mu.Unlock()
	testFS.mapFS[newname] = testFS.mapFS[oldname]
	if !oldFileInfo.IsDir() {
		return nil
	}
	dirPrefix := oldname + "/"
	for name, file := range testFS.mapFS {
		if strings.HasPrefix(name, dirPrefix) {
			testFS.mapFS[path.Join(newname, strings.Trim(name, dirPrefix))] = file
		}
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
		return fmt.Errorf("cannot write to %[1]q: %[1]q already exists and is a directory", testFile.name)
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
