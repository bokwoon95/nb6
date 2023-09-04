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

func (nbrew *Notebrew) rename(w http.ResponseWriter, r *http.Request) {
	type Request struct {
		ParentFolder string `json:"parent_folder,omitempty"`
		OldName      string `json:"old_name,omitempty"`
		NewName      string `json:"new_name,omitempty"`
	}
	type Response struct {
		ParentFolder string     `json:"parent_folder,omitempty"`
		OldName      string     `json:"old_name,omitempty"`
		NewName      string     `json:"new_name,omitempty"`
		Errors       url.Values `json:"errors,omitempty"`
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
			response.ParentFolder = r.Form.Get("parent_folder")
			response.OldName = r.Form.Get("old_name")
			response.NewName = r.Form.Get("new_name")
		}
		if response.ParentFolder != "" {
			response.ParentFolder = strings.Trim(path.Clean(response.ParentFolder), "/")
		}
		nbrew.clearSession(w, r, "flash")

		tmpl, err := template.ParseFS(rootFS, "html/rename.html")
		if err != nil {
			logger.Error(err.Error())
			internalServerError(w, r, err)
			return
		}
		buf := bufPool.Get().(*bytes.Buffer)
		buf.Reset()
		defer bufPool.Put(buf)
		err = tmpl.Execute(buf, &response)
		if err != nil {
			logger.Error(err.Error())
			internalServerError(w, r, err)
			return
		}
		w.Header().Add("Content-Security-Policy", defaultContentSecurityPolicy)
		buf.WriteTo(w)
	case "POST":
		writeResponse := func(w http.ResponseWriter, r *http.Request, response Response) {
			accept, _, _ := mime.ParseMediaType(r.Header.Get("Accept"))
			if accept == "application/json" {
				w.Header().Set("Content-Type", "application/json")
				b, err := json.Marshal(&response)
				if err != nil {
					logger.Error(err.Error())
					internalServerError(w, r, err)
					return
				}
				w.Write(b)
				return
			}
			if len(response.Errors) > 0 {
				err := nbrew.setSession(w, r, "flash", &response)
				if err != nil {
					logger.Error(err.Error())
					internalServerError(w, r, err)
					return
				}
				http.Redirect(w, r, r.URL.String(), http.StatusFound)
				return
			}
			http.Redirect(w, r, nbrew.Scheme+nbrew.AdminDomain+"/"+path.Join("admin", sitePrefix, response.ParentFolder)+"/", http.StatusFound)
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
				internalServerError(w, r, err)
				return
			}
		case "application/x-www-form-urlencoded":
			err := r.ParseForm()
			if err != nil {
				http.Error(w, fmt.Sprintf("400 Bad Request: %s", err), http.StatusBadRequest)
				return
			}
			request.ParentFolder = r.Form.Get("parent_folder")
			request.OldName = r.Form.Get("old_name")
			request.NewName = r.Form.Get("new_name")
		default:
			http.Error(w, "415 Unsupported Media Type", http.StatusUnsupportedMediaType)
			return
		}

		response := Response{
			ParentFolder: request.ParentFolder,
			OldName:      request.OldName,
			NewName:      request.NewName,
			Errors:       make(url.Values),
		}
		if response.ParentFolder == "" {
			response.Errors.Add("parent_folder", "cannot be empty")
		} else {
			response.ParentFolder = strings.Trim(path.Clean(response.ParentFolder), "/")
		}
		if response.OldName == "" {
			response.Errors.Add("old_name", "cannot be empty")
		}
		if response.NewName == "" {
			response.Errors.Add("new_name", "cannot be empty")
		} else {
			errmsgs := validateName(response.NewName)
			if len(errmsgs) > 0 {
				response.Errors["new_name"] = append(response.Errors["new_name"], errmsgs...)
			}
		}
		if len(response.Errors) > 0 {
			writeResponse(w, r, response)
			return
		}

		fileInfo, err := fs.Stat(nbrew.FS, path.Join(sitePrefix, response.ParentFolder))
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				response.Errors.Add("parent_folder", "folder does not exist")
				writeResponse(w, r, response)
				return
			}
			logger.Error(err.Error())
			internalServerError(w, r, err)
			return
		}
		if !fileInfo.IsDir() {
			response.Errors.Add("parent_folder", "not a folder")
			writeResponse(w, r, response)
			return
		}

		oldPath := path.Join(sitePrefix, response.ParentFolder, response.OldName)
		_, err = fs.Stat(nbrew.FS, oldPath)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				response.Errors.Add("old_name", "file/folder does not exist")
				writeResponse(w, r, response)
				return
			}
			logger.Error(err.Error())
			internalServerError(w, r, err)
			return
		}

		newPath := path.Join(sitePrefix, response.ParentFolder, response.NewName)
		_, err = fs.Stat(nbrew.FS, newPath)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			logger.Error(err.Error())
			internalServerError(w, r, err)
			return
		}
		if err == nil {
			response.Errors.Add("new_name", "file/folder already exists")
			writeResponse(w, r, response)
			return
		}

		err = nbrew.FS.Rename(oldPath, newPath)
		if err != nil {
			internalServerError(w, r, err)
			return
		}
		writeResponse(w, r, response)
	default:
		http.Error(w, "405 Method Not Allowed", http.StatusMethodNotAllowed)
	}
}
