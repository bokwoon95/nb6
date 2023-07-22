package nb6

import (
	"bytes"
	"compress/gzip"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"

	"golang.org/x/crypto/blake2b"
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
	ext := path.Ext(name)
	if ext == ".gz" {
		ext = path.Ext(strings.TrimSuffix(name, ".gz"))
	}
	if ext != ".css" && ext != ".js" {
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
	buf := bufPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer bufPool.Put(buf)
	hash, err := blake2b.New256(nil)
	if err != nil {
		logger.Error(err.Error())
		http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
		return
	}
	multiWriter := io.MultiWriter(buf, hash)
	// TODO: we determine the file extension here and set the Content-Type
	// ourselves directly instead of relying on http.ServeContent to do so for
	// us. Then we can pass in an empty name for the string (no need to
	// TrimSuffix ".gz") and we can pass in gzipped bytes to the bytes.Reader.
	if strings.HasSuffix(name, ".gz") {
		_, err = io.Copy(multiWriter, file)
		if err != nil {
			logger.Error(err.Error())
			http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
			return
		}
	} else {
		gzipWriter := gzipPool.Get().(*gzip.Writer)
		gzipWriter.Reset(multiWriter)
		defer gzipWriter.Close()
		_, err = io.Copy(gzipWriter, file)
		if err != nil {
			logger.Error(err.Error())
			http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
			return
		}
		err = gzipWriter.Close()
		if err != nil {
			logger.Error(err.Error())
			http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
			return
		}
	}
	w.Header().Set("Content-Encoding", "gzip")
	w.Header().Set("ETag", hex.EncodeToString(hash.Sum(nil)))
	http.ServeContent(w, r, strings.TrimSuffix(name, ".gz"), fileInfo.ModTime(), bytes.NewReader(buf.Bytes()))
}
