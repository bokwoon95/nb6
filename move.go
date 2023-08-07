package nb6

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strings"

	"golang.org/x/exp/slog"
)

func (nbrew *Notebrew) move(w http.ResponseWriter, r *http.Request) {
	type Request struct {
		Path              string `json:"path,omitempty"`
		DestinationFolder string `json:"destination_folder,omitempty"`
	}
	type Response struct {
		Path              string     `json:"path,omitempty"`
		DestinationFolder string     `json:"destination_folder,omitempty"`
		Errors            url.Values `json:"errors,omitempty"`
	}

	logger, ok := r.Context().Value(loggerKey).(*slog.Logger)
	if !ok {
		logger = slog.Default()
	}

	var sitePrefix string
	segments := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(segments) > 1 && (strings.HasPrefix(segments[1], "@") || strings.Contains(segments[1], ".")) {
		sitePrefix = segments[1]
	}

	switch r.Method {
	case "GET":
		err := r.ParseForm()
		if err != nil {
			http.Error(w, fmt.Sprintf("400 Bad Request: %s", err), http.StatusBadRequest)
			return
		}
		var response Response
		ok, err := nbrew.getSession(r, "flash_session", &response)
		if err != nil {
			logger.Error(err.Error())
		}
		if !ok {
			response.Path = r.Form.Get("path")
			response.DestinationFolder = r.Form.Get("destination_folder")
		}
		if response.Path != "" {
			response.Path = strings.Trim(path.Clean(response.Path), "/")
		}
		if response.DestinationFolder != "" {
			response.DestinationFolder = strings.Trim(path.Clean(response.DestinationFolder), "/")
		}
		nbrew.clearSession(w, r, "flash_session")

		tmpl, err := template.ParseFS(rootFS, "html/move.html")
		if err != nil {
			logger.Error(err.Error())
			http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
			return
		}
		buf := bufPool.Get().(*bytes.Buffer)
		buf.Reset()
		defer bufPool.Put(buf)
		err = tmpl.Execute(buf, &response)
		if err != nil {
			logger.Error(err.Error())
			http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
			return
		}
		buf.WriteTo(w)
	case "POST":
		writeResponse := func(w http.ResponseWriter, r *http.Request, response Response) {
			accept, _, _ := mime.ParseMediaType(r.Header.Get("Accept"))
			if accept == "application/json" {
				b, err := json.Marshal(&response)
				if err != nil {
					logger.Error(err.Error())
					http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
					return
				}
				w.Write(b)
				return
			}
			if len(response.Errors) > 0 {
				err := nbrew.setSession(w, r, "flash", &response)
				if err != nil {
					logger.Error(err.Error())
					http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
					return
				}
				http.Redirect(w, r, r.URL.String(), http.StatusFound)
				return
			}
			http.Redirect(w, r, "/"+path.Join("admin", sitePrefix, response.DestinationFolder)+"/", http.StatusFound)
		}

		var request Request
		contentType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		switch contentType {
		case "application/json":
			err := json.NewDecoder(r.Body).Decode(&request)
			if err != nil {
				var syntaxErr *json.SyntaxError
				if errors.As(err, &syntaxErr) {
					http.Error(w, "400 Bad Request: invalid JSON", http.StatusBadRequest)
					return
				}
				logger.Error(err.Error())
				http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
				return
			}
		case "application/x-www-form-urlencoded":
			err := r.ParseForm()
			if err != nil {
				http.Error(w, fmt.Sprintf("400 Bad Request: %s", err), http.StatusBadRequest)
				return
			}
			request.Path = r.Form.Get("path")
			request.DestinationFolder = r.Form.Get("destination_folder")
		default:
			http.Error(w, "415 Unsupported Media Type", http.StatusUnsupportedMediaType)
			return
		}

		response := Response{
			Path:              request.Path,
			DestinationFolder: request.DestinationFolder,
			Errors:            make(url.Values),
		}
		if response.Path == "" {
			response.Errors.Add("path", "cannot be empty")
		} else {
			response.Path = strings.Trim(path.Clean(response.Path), "/")
		}
		if response.DestinationFolder == "" {
			response.Errors.Add("destination_folder", "cannot be empty")
		} else {
			response.DestinationFolder = strings.Trim(path.Clean(response.DestinationFolder), "/")
		}
		if len(response.Errors) > 0 {
			writeResponse(w, r, response)
			return
		}

		srcPath := path.Join(sitePrefix, response.Path)
		fileInfo, err := fs.Stat(nbrew.FS, srcPath)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				response.Errors.Add("path", "file or folder does not exist")
				writeResponse(w, r, response)
				return
			}
			logger.Error(err.Error())
			http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
			return
		}
		srcIsDir := fileInfo.IsDir()

		destFolder := path.Join(sitePrefix, response.DestinationFolder)
		fileInfo, err = fs.Stat(nbrew.FS, destFolder)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				response.Errors.Add("destination_folder", "folder does not exist")
				writeResponse(w, r, response)
				return
			}
			logger.Error(err.Error())
			http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
			return
		}
		if !fileInfo.IsDir() {
			response.Errors.Add("destination_folder", "not a folder")
			writeResponse(w, r, response)
			return
		}

		destPath := path.Join(destFolder, path.Base(response.Path))
		_, err = fs.Stat(nbrew.FS, destPath)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			logger.Error(err.Error())
			http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
			return
		}
		if err == nil {
			response.Errors.Add("path", "file already exists in destination folder")
			writeResponse(w, r, response)
			return
		}

		if !srcIsDir {
			err = nbrew.FS.Rename(srcPath, destPath)
			if err != nil {
				logger.Error(err.Error())
				http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
				return
			}
			writeResponse(w, r, response)
			return
		}

		dirEntries, err := nbrew.FS.ReadDir(srcPath)
		if err != nil {
			logger.Error(err.Error())
			http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
			return
		}
		if len(dirEntries) == 0 {
			err = nbrew.FS.Rename(srcPath, destPath)
			if err != nil {
				logger.Error(err.Error())
				http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
				return
			}
			writeResponse(w, r, response)
			return
		}

		type Item struct {
			RelativePath string // path relative to srcPath/destPath
			IsFile       bool
		}
		pushItems := func(items []Item, dir string, dirEntries []fs.DirEntry) []Item {
			for i := len(dirEntries) - 1; i >= 0; i-- {
				dirEntry := dirEntries[i]
				items = append(items, Item{
					RelativePath: path.Join(dir, dirEntry.Name()),
					IsFile:       !dirEntry.IsDir(),
				})
			}
			return items
		}
		var item Item
		items := pushItems(nil, "", dirEntries)
		for len(items) > 0 {
			item, items = items[len(items)-1], items[:len(items)-1]
			if item.IsFile {
				err = nbrew.FS.Rename(path.Join(srcPath, item.RelativePath), path.Join(destPath, item.RelativePath))
				if err != nil {
					logger.Error(err.Error())
					http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
					return
				}
				continue
			}
			err = nbrew.FS.Mkdir(path.Join(destPath, item.RelativePath), 0755)
			if err != nil {
				logger.Error(err.Error())
				http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
				return
			}
			dirEntries, err := nbrew.FS.ReadDir(path.Join(srcPath, item.RelativePath))
			if err != nil {
				logger.Error(err.Error())
				http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
				return
			}
			items = pushItems(items, item.RelativePath, dirEntries)
		}
		writeResponse(w, r, response)
	default:
		http.Error(w, "405 Method Not Allowed", http.StatusMethodNotAllowed)
	}
}
