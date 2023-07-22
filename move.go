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

func (nbrew *Notebrew) move(w http.ResponseWriter, r *http.Request) {
	type Request struct {
		Path              string `json:"path,omitempty"`
		DestinationFolder string `json:"destination_folder,omitempty"`
	}
	type Response struct {
		Path                    string   `json:"path,omitempty"`
		PathErrors              []string `json:"path_errors,omitempty"`
		DestinationFolder       string   `json:"destination_folder,omitempty"`
		DestinationFolderErrors []string `json:"destination_folder_errors,omitempty"`
		Error                   string   `json:"error,omitempty"`
	}

	var sitePrefix string
	// E.g. /admin/@bokwoon/move/
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
			if len(response.PathErrors) == 0 && len(response.DestinationFolderErrors) == 0 && response.Error == "" {
				http.Redirect(w, r, "/"+path.Join("admin", sitePrefix, response.DestinationFolder)+"/", http.StatusFound)
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
			request.Path = r.Form.Get("path")
			request.DestinationFolder = r.Form.Get("destination_folder")
		default:
			http.Error(w, "415 Unsupported Media Type", http.StatusUnsupportedMediaType)
			return
		}

		response := Response{
			Path:              request.Path,
			DestinationFolder: request.DestinationFolder,
		}
		if response.Path == "" {
			response.PathErrors = append(response.PathErrors, "cannot be empty")
		} else {
			response.Path = strings.Trim(path.Clean(response.Path), "/")
		}
		if response.DestinationFolder == "" {
			response.DestinationFolderErrors = append(response.DestinationFolderErrors, "cannot be empty")
		} else {
			response.DestinationFolder = strings.Trim(path.Clean(response.DestinationFolder), "/")
		}
		if len(response.PathErrors) > 0 || len(response.DestinationFolderErrors) > 0 {
			writeResponse(w, r, response)
			return
		}

		fileInfo, err := fs.Stat(nbrew.FS, path.Join(sitePrefix, response.DestinationFolder))
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				response.DestinationFolderErrors = append(response.DestinationFolderErrors, "folder does not exist")
				writeResponse(w, r, response)
				return
			}
			logger.Error(err.Error())
			http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
			return
		}
		if !fileInfo.IsDir() {
			response.DestinationFolderErrors = append(response.DestinationFolderErrors, "not a folder")
			writeResponse(w, r, response)
			return
		}

		// TODO: need to handle both moving a file and moving a folder :/.
	default:
		http.Error(w, "405 Method Not Allowed", http.StatusMethodNotAllowed)
	}
}
