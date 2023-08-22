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
	"strconv"
	"strings"

	"golang.org/x/exp/slog"
)

func (nbrew *Notebrew) delet(w http.ResponseWriter, r *http.Request) {
	type Request struct {
		Path      string `json:"path,omitempty"`
		Recursive bool   `json:"recursive,omitempty"`
	}
	type Response struct {
		Path             string     `json:"path,omitempty"`
		IsNonEmptyFolder bool       `json:"is_non_empty_folder,omitempty"`
		Recursive        bool       `json:"recursive,omitempty"`
		Errors           url.Values `json:"errors,omitempty"`
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
		ok, err := nbrew.getSession(r, "flash", &response)
		if err != nil {
			logger.Error(err.Error())
		} else if !ok {
			response.Path = r.Form.Get("path")
			response.Recursive, _ = strconv.ParseBool(r.Form.Get("recursive"))
		}
		if response.Path != "" {
			response.Path = strings.Trim(path.Clean(response.Path), "/")
		}
		nbrew.clearSession(w, r, "flash")

		tmpl, err := template.ParseFS(rootFS, "html/delete.html")
		if err != nil {
			logger.Error(err.Error())
			http.Error(w, messageInternalServerError, http.StatusInternalServerError)
			return
		}
		buf := bufPool.Get().(*bytes.Buffer)
		buf.Reset()
		defer bufPool.Put(buf)
		err = tmpl.Execute(buf, &response)
		if err != nil {
			logger.Error(err.Error())
			http.Error(w, messageInternalServerError, http.StatusInternalServerError)
			return
		}
		buf.WriteTo(w)
	case "POST":
		writeResponse := func(w http.ResponseWriter, r *http.Request, response Response) {
			accept, _, _ := mime.ParseMediaType(r.Header.Get("Accept"))
			if accept == "application/json" {
				if len(response.Errors) == 0 && response.IsNonEmptyFolder && !response.Recursive {
					response.Errors.Set("", "cannot delete non-empty folder unless \"recursive\" property is set to true")
				}
				b, err := json.Marshal(&response)
				if err != nil {
					logger.Error(err.Error())
					http.Error(w, messageInternalServerError, http.StatusInternalServerError)
					return
				}
				w.Write(b)
				return
			}
			if len(response.Errors) > 0 || (response.IsNonEmptyFolder && !response.Recursive) {
				err := nbrew.setSession(w, r, "flash", &response)
				if err != nil {
					logger.Error(err.Error())
					http.Error(w, messageInternalServerError, http.StatusInternalServerError)
					return
				}
				http.Redirect(w, r, r.URL.String(), http.StatusFound)
				return
			}
			http.Redirect(w, r, nbrew.Protocol+nbrew.AdminDomain+"/"+path.Join("admin", sitePrefix, path.Dir(response.Path))+"/", http.StatusFound)
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
				http.Error(w, messageInternalServerError, http.StatusInternalServerError)
				return
			}
		case "application/x-www-form-urlencoded":
			err := r.ParseForm()
			if err != nil {
				http.Error(w, fmt.Sprintf("400 Bad Request: %s", err), http.StatusBadRequest)
				return
			}
			request.Path = r.Form.Get("path")
			request.Recursive, _ = strconv.ParseBool(r.Form.Get("recursive"))
		default:
			http.Error(w, "415 Unsupported Media Type", http.StatusUnsupportedMediaType)
			return
		}

		response := Response{
			Path:      request.Path,
			Recursive: request.Recursive,
			Errors:    make(url.Values),
		}
		if response.Path != "" {
			response.Path = strings.Trim(path.Clean(response.Path), "/")
		}
		filePath := path.Join(sitePrefix, response.Path)

		fileInfo, err := fs.Stat(nbrew.FS, filePath)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				response.Errors.Add("path", "file or folder does not exist")
				writeResponse(w, r, response)
				return
			}
			logger.Error(err.Error())
			http.Error(w, messageInternalServerError, http.StatusInternalServerError)
			return
		}

		if !fileInfo.IsDir() {
			err = nbrew.FS.Remove(filePath)
			if err != nil {
				logger.Error(err.Error())
				http.Error(w, messageInternalServerError, http.StatusInternalServerError)
				return
			}
			writeResponse(w, r, response)
			return
		}

		dirEntries, err := nbrew.FS.ReadDir(filePath)
		if err != nil {
			logger.Error(err.Error())
			http.Error(w, messageInternalServerError, http.StatusInternalServerError)
			return
		}
		if len(dirEntries) == 0 {
			err = nbrew.FS.Remove(filePath)
			if err != nil {
				logger.Error(err.Error())
				http.Error(w, messageInternalServerError, http.StatusInternalServerError)
				return
			}
			writeResponse(w, r, response)
			return
		}
		response.IsNonEmptyFolder = true
		if !response.Recursive {
			writeResponse(w, r, response)
			return
		}

		if fsys, ok := nbrew.FS.(interface{ RemoveAll(name string) error }); ok {
			err = fsys.RemoveAll(filePath)
			if err != nil {
				logger.Error(err.Error())
				http.Error(w, messageInternalServerError, http.StatusInternalServerError)
				return
			}
			writeResponse(w, r, response)
			return
		}

		type Item struct {
			RelativePath string // path relative to filePath
			IsFile       bool
			IsEmptyDir   bool
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
			if item.IsFile || item.IsEmptyDir {
				err = nbrew.FS.Remove(path.Join(filePath, item.RelativePath))
				if err != nil {
					logger.Error(err.Error())
					http.Error(w, messageInternalServerError, http.StatusInternalServerError)
					return
				}
				continue
			}
			items = append(items, Item{
				RelativePath: item.RelativePath,
				IsEmptyDir:   true,
			})
			dirEntries, err := nbrew.FS.ReadDir(path.Join(filePath, item.RelativePath))
			if err != nil {
				logger.Error(err.Error())
				http.Error(w, messageInternalServerError, http.StatusInternalServerError)
				return
			}
			items = pushItems(items, item.RelativePath, dirEntries)
		}
		writeResponse(w, r, response)
	default:
		http.Error(w, "405 Method Not Allowed", http.StatusMethodNotAllowed)
	}
}
