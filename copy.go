package nb6

import (
	"context"
	"io"
	"io/fs"
)

// copyFile copies src to dst like the cp command.
func copyFile(ctx context.Context, fsys FS, srcName, destName string) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if destName == srcName {
		return fs.ErrInvalid
	}
	srcFile, err := fsys.Open(srcName)
	if err != nil {
		return err
	}
	defer srcFile.Close()
	srcFileInfo, err := srcFile.Stat()
	if err != nil {
		return err
	}
	destFile, err := fsys.OpenWriter(destName, srcFileInfo.Mode())
	if err != nil {
		return err
	}
	defer destFile.Close()
	_, err = io.Copy(destFile, &contextReader{
		ctx: ctx,
		src: srcFile,
	})
	if err != nil {
		return err
	}
	return destFile.Close()
}

type contextReader struct {
	ctx context.Context
	src io.Reader
}

func (ctxReader *contextReader) Read(p []byte) (n int, err error) {
	err = ctxReader.ctx.Err()
	if err != nil {
		return 0, err
	}
	return ctxReader.src.Read(p)
}
