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

	if r.Method != "GET" {
		http.Error(w, "405 Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	urlPath := strings.Trim(strings.TrimPrefix(r.URL.Path, "/admin"), "/")
	ext := path.Ext(urlPath)
	if ext == ".gz" {
		ext = path.Ext(strings.TrimSuffix(urlPath, ext))
	}
	if ext != ".html" && ext != ".css" && ext != ".js" && ext != ".png" {
		http.Error(w, "404 Not Found", http.StatusNotFound)
		return
	}

	file, err := rootFS.Open(urlPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			http.Error(w, "404 Not Found", http.StatusNotFound)
			return
		}
		logger.Error(err.Error())
		http.Error(w, messageInternalServerError, http.StatusInternalServerError)
		return
	}
	defer file.Close()

	fileInfo, err := file.Stat()
	if err != nil {
		logger.Error(err.Error())
		http.Error(w, messageInternalServerError, http.StatusInternalServerError)
		return
	}
	if fileInfo.IsDir() {
		http.Error(w, "404 Not Found", http.StatusNotFound)
		return
	}

	buf := bufPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer bufPool.Put(buf)

	if ext != ".html" && ext != ".css" && ext != ".js" {
		fileSeeker, ok := file.(io.ReadSeeker)
		if ok {
			http.ServeContent(w, r, strings.TrimSuffix(urlPath, ".gz"), fileInfo.ModTime(), fileSeeker)
			return
		}
		_, err = buf.ReadFrom(file)
		if err != nil {
			logger.Error(err.Error())
			http.Error(w, messageInternalServerError, http.StatusInternalServerError)
			return
		}
		http.ServeContent(w, r, strings.TrimSuffix(urlPath, ".gz"), fileInfo.ModTime(), bytes.NewReader(buf.Bytes()))
		return
	}

	hash, err := blake2b.New256(nil)
	if err != nil {
		logger.Error(err.Error())
		http.Error(w, messageInternalServerError, http.StatusInternalServerError)
		return
	}

	// Write gzipped data into both the buffer and hash. If the file is already
	// gzipped, we can write the file contents as-is. Otherwise, we first pass
	// the file contents through a gzipWriter.
	multiWriter := io.MultiWriter(buf, hash)
	if strings.HasSuffix(urlPath, ".gz") {
		_, err = io.Copy(multiWriter, file)
		if err != nil {
			logger.Error(err.Error())
			http.Error(w, messageInternalServerError, http.StatusInternalServerError)
			return
		}
	} else {
		gzipWriter := gzipPool.Get().(*gzip.Writer)
		gzipWriter.Reset(multiWriter)
		defer gzipPool.Put(gzipWriter)
		_, err = io.Copy(gzipWriter, file)
		if err != nil {
			logger.Error(err.Error())
			http.Error(w, messageInternalServerError, http.StatusInternalServerError)
			return
		}
		err = gzipWriter.Close()
		if err != nil {
			logger.Error(err.Error())
			http.Error(w, messageInternalServerError, http.StatusInternalServerError)
			return
		}
	}

	w.Header().Set("Content-Encoding", "gzip")
	w.Header().Set("ETag", hex.EncodeToString(hash.Sum(nil)))
	http.ServeContent(w, r, strings.TrimSuffix(urlPath, ".gz"), fileInfo.ModTime(), bytes.NewReader(buf.Bytes()))
}
