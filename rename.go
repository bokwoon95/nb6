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
		ParentFolder       string   `json:"parent_folder,omitempty"`
		ParentFolderErrors []string `json:"parent_folder_errors,omitempty"`
		OldName            string   `json:"old_name,omitempty"`
		OldNameErrors      []string `json:"old_name_errors,omitempty"`
		NewName            string   `json:"new_name,omitempty"`
		NewNameErrors      []string `json:"new_name_errors,omitempty"`
		Error              string   `json:"error,omitempty"`
	}

	var sitePrefix string
	// E.g. /admin/@bokwoon/rename/
	_, tail, _ := strings.Cut(strings.Trim(r.URL.Path, "/"), "/")
	head, _, _ := strings.Cut(strings.Trim(tail, "/"), "/")
	if strings.HasPrefix(head, "@") || strings.Contains(head, ".") {
		sitePrefix = head
	}

	logger, ok := r.Context().Value(loggerKey).(*slog.Logger)
	if !ok {
		logger = slog.Default()
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
			response.ParentFolder = r.Form.Get("parent_folder")
			response.OldName = r.Form.Get("old_name")
			response.NewName = r.Form.Get("new_name")
		}
		if response.ParentFolder != "" {
			response.ParentFolder = strings.Trim(path.Clean(response.ParentFolder), "/")
		}
		nbrew.clearSession(w, r, "flash_session")
		tmpl, err := template.ParseFS(rootFS, "html/rename.html")
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
			if len(response.ParentFolderErrors) == 0 && len(response.OldNameErrors) == 0 && len(response.NewNameErrors) == 0 && response.Error == "" {
				http.Redirect(w, r, "/"+path.Join("admin", sitePrefix, response.ParentFolder)+"/", http.StatusFound)
				return
			}
			err := nbrew.setSession(w, r, &response, &http.Cookie{
				Path:     r.URL.Path,
				Name:     "flash_session",
				Secure:   nbrew.Scheme == "https://",
				HttpOnly: true,
				SameSite: http.SameSiteLaxMode,
			})
			if err != nil {
				logger.Error(err.Error())
				http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
				return
			}
			http.Redirect(w, r, r.URL.String(), http.StatusFound)
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
		}
		if response.ParentFolder == "" {
			response.ParentFolderErrors = append(response.ParentFolderErrors, "cannot be empty")
		} else {
			response.ParentFolder = strings.Trim(path.Clean(response.ParentFolder), "/")
		}
		if response.OldName == "" {
			response.OldNameErrors = append(response.OldNameErrors, "cannot be empty")
		}
		if response.NewName == "" {
			response.NewNameErrors = append(response.NewNameErrors, "cannot be empty")
		} else {
			response.NewNameErrors = validateName(response.NewName)
		}
		if len(response.ParentFolderErrors) > 0 || len(response.OldNameErrors) > 0 || len(response.NewNameErrors) > 0 {
			writeResponse(w, r, response)
			return
		}

		fileInfo, err := fs.Stat(nbrew.FS, path.Join(sitePrefix, response.ParentFolder))
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				response.ParentFolderErrors = append(response.ParentFolderErrors, "folder does not exist")
				writeResponse(w, r, response)
				return
			}
			logger.Error(err.Error())
			http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
			return
		}
		if !fileInfo.IsDir() {
			response.ParentFolderErrors = append(response.ParentFolderErrors, "not a folder")
			writeResponse(w, r, response)
			return
		}

		_, err = fs.Stat(nbrew.FS, path.Join(sitePrefix, response.ParentFolder, response.OldName))
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				response.OldNameErrors = append(response.OldNameErrors, "old file/folder does not exist")
				writeResponse(w, r, response)
				return
			}
			logger.Error(err.Error())
			http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
			return
		}

		_, err = fs.Stat(nbrew.FS, path.Join(sitePrefix, response.ParentFolder, response.NewName))
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			logger.Error(err.Error())
			http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
			return
		}
		if err == nil {
			response.NewNameErrors = append(response.NewNameErrors, "new file/folder already exists")
			writeResponse(w, r, response)
			return
		}

		err = nbrew.FS.Rename(path.Join(sitePrefix, response.ParentFolder, response.OldName), path.Join(sitePrefix, response.ParentFolder, response.NewName))
		if err != nil {
			http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
			return
		}
		writeResponse(w, r, response)
	default:
		http.Error(w, "405 Method Not Allowed", http.StatusMethodNotAllowed)
	}
}
