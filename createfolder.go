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

func (nbrew *Notebrew) createfolder(w http.ResponseWriter, r *http.Request) {
	type Request struct {
		ParentFolder string `json:"parent_folder,omitempty"`
		Name         string `json:"name,omitempty"`
	}
	type Response struct {
		ParentFolder       string   `json:"parent_folder,omitempty"`
		ParentFolderErrors []string `json:"parent_folder_errors,omitempty"`
		Name               string   `json:"name,omitempty"`
		NameErrors         []string `json:"name_errors,omitempty"`
		Error              string   `json:"error,omitempty"`
		AlreadyExists      string   `json:"already_exists,omitempty"`
	}

	var sitePrefix string
	// E.g. /admin/@bokwoon/createfolder/
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
		ok, err := nbrew.getSession(r, "flash", &response)
		if err != nil {
			logger.Error(err.Error())
		}
		if !ok {
			response.ParentFolder = r.Form.Get("parent_folder")
			response.Name = r.Form.Get("name")
		}
		if response.ParentFolder != "" {
			response.ParentFolder = strings.Trim(path.Clean(response.ParentFolder), "/")
		}
		nbrew.clearSession(w, r, "flash")
		tmpl, err := template.ParseFS(rootFS, "html/createfolder.html")
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
			request.Name = r.Form.Get("name")
		default:
			http.Error(w, "415 Unsupported Media Type", http.StatusUnsupportedMediaType)
			return
		}

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
			if len(response.ParentFolderErrors) == 0 && len(response.NameErrors) == 0 && response.Error == "" && response.AlreadyExists == "" {
				http.Redirect(w, r, "/"+path.Join("admin", sitePrefix, response.ParentFolder, response.Name)+"/", http.StatusFound)
				return
			}
			err := nbrew.setSession(w, r, &response, &http.Cookie{
				Path:     r.URL.Path,
				Name:     "flash",
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

		response := Response{
			ParentFolder: request.ParentFolder,
			Name:         request.Name,
		}
		if response.ParentFolder != "" {
			response.ParentFolder = strings.Trim(path.Clean(response.ParentFolder), "/")
		}
		head, tail, _ := strings.Cut(response.ParentFolder, "/")

		if head != "posts" && head != "notes" && head != "pages" && head != "templates" && head != "assets" {
			response.ParentFolderErrors = append(response.ParentFolderErrors, "parent folder has to start with posts, notes, pages, templates or assets")
		} else if (head == "posts" || head == "notes") && tail != "" {
			response.ParentFolderErrors = append(response.ParentFolderErrors, "not allowed to use this parent folder")
		}

		if response.Name == "" {
			response.NameErrors = append(response.NameErrors, "cannot be empty")
		} else {
			response.NameErrors = validateName(response.NameErrors, response.Name)
		}

		if len(response.ParentFolderErrors) > 0 || len(response.NameErrors) > 0 {
			writeResponse(w, r, response)
			return
		}

		_, err := fs.Stat(nbrew.FS, path.Join(sitePrefix, response.ParentFolder))
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				logger.Error(err.Error())
				http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
				return
			}
			response.ParentFolderErrors = append(response.ParentFolderErrors, "folder does not exist")
			writeResponse(w, r, response)
			return
		}

		fileInfo, err := fs.Stat(nbrew.FS, path.Join(sitePrefix, response.ParentFolder, response.Name))
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			logger.Error(err.Error())
			http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
			return
		}
		if err == nil {
			if fileInfo.IsDir() {
				response.AlreadyExists = "/" + path.Join("admin", sitePrefix, response.ParentFolder, response.Name)
			} else {
				response.NameErrors = append(response.NameErrors, "file with the same name already exists")
			}
			writeResponse(w, r, response)
			return
		}

		err = nbrew.FS.Mkdir(path.Join(sitePrefix, response.ParentFolder, response.Name))
		if err != nil {
			logger.Error(err.Error())
			http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
			return
		}
		writeResponse(w, r, response)
	default:
		http.Error(w, "405 Method Not Allowed", http.StatusMethodNotAllowed)
	}
}
