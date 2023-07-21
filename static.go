package nb6

import (
	"errors"
	"io"
	"io/fs"
	"net/http"
	"strings"

	"golang.org/x/exp/slog"
)

func (nbrew *Notebrew) static(w http.ResponseWriter, r *http.Request) {
	logger, ok := r.Context().Value(loggerKey).(*slog.Logger)
	if !ok {
		logger = slog.Default()
	}
	// Example: /admin/static/abcd
	if r.Method != "GET" {
		http.Error(w, "405 Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	_, name, _ := strings.Cut(strings.Trim(r.URL.Path, "/"), "/")
	head, _, _ := strings.Cut(name, "/")
	if head != "static" {
		http.Error(w, "404 Not Found", http.StatusNotFound)
		return
	}
	file, err := rootFS.Open(name)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			http.Error(w, "404 Not Found", http.StatusNotFound)
			return
		}
		logger.Error(err.Error())
		http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
		return
	}
	defer file.Close()
	fileInfo, err := file.Stat()
	if err != nil {
		logger.Error(err.Error())
		http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
		return
	}
	if fileInfo.IsDir() {
		http.Error(w, "404 Not Found", http.StatusNotFound)
		return
	}
	// TODO: if the file doesn't end in gz, add a gzip pipeline to it. Else we
	// can write the contents of the gzipped file as-is into the
	// io.MultiWriter. The blake2b hash always sees gzipped data, because the
	// user always sees gzipped data.
	// TODO: use ETags instead. Write file contents into a buffer while
	// simultaneously writing into a blake2b hash.Hash, then serve both the
	// ETag header and the buffer's contents out to the user.
	fileseeker, ok := file.(io.ReadSeeker)
	if ok {
		http.ServeContent(w, r, name, fileInfo.ModTime(), fileseeker)
		return
	}
}
