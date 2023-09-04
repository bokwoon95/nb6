package nb6

import (
	"context"
	"io"
	"io/fs"
	"net/http"
)

func (nbrew *Notebrew) cpy(w http.ResponseWriter, r *http.Request) {
}

// copyFile copies src to dst like the cp command.
func copyFile(ctx context.Context, fsys FS, srcName, destName string) error {
	err := ctx.Err()
	if err != nil {
		return err
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
	readerFrom, err := fsys.OpenReaderFrom(destName, srcFileInfo.Mode())
	if err != nil {
		return err
	}
	_, err = readerFrom.ReadFrom(&contextReader{
		ctx: ctx,
		src: srcFile,
	})
	return err
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
