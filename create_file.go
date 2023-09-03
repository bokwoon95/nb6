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

	"github.com/oklog/ulid/v2"
	"golang.org/x/exp/slices"
	"golang.org/x/exp/slog"
)

func (nbrew *Notebrew) createFile(w http.ResponseWriter, r *http.Request) {
	type Request struct {
		ParentFolder string `json:"parent_folder,omitempty"`
		Name         string `json:"name,omitempty"`
	}
	type Response struct {
		ParentFolder  string     `json:"parent_folder,omitempty"`
		Name          string     `json:"name,omitempty"`
		Errors        url.Values `json:"errors,omitempty"`
		AlreadyExists string     `json:"already_exists,omitempty"`
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
			response.Name = r.Form.Get("name")
		}
		if response.ParentFolder != "" {
			response.ParentFolder = strings.Trim(path.Clean(response.ParentFolder), "/")
		}
		nbrew.clearSession(w, r, "flash")

		tmpl, err := template.ParseFS(rootFS, "html/create_file.html")
		if err != nil {
			logger.Error(err.Error())
			internalServerError(w, r)
			return
		}
		buf := bufPool.Get().(*bytes.Buffer)
		buf.Reset()
		defer bufPool.Put(buf)
		err = tmpl.Execute(buf, &response)
		if err != nil {
			logger.Error(err.Error())
			internalServerError(w, r)
			return
		}
		w.Header().Add("Content-Security-Policy", defaultContentSecurityPolicy)
		buf.WriteTo(w)
	case "POST":
		writeResponse := func(w http.ResponseWriter, r *http.Request, response Response) {
			accept, _, _ := mime.ParseMediaType(r.Header.Get("Accept"))
			if accept == "application/json" {
				b, err := json.Marshal(&response)
				if err != nil {
					logger.Error(err.Error())
					internalServerError(w, r)
					return
				}
				w.Write(b)
				return
			}
			if len(response.Errors) > 0 {
				err := nbrew.setSession(w, r, "flash", &response)
				if err != nil {
					logger.Error(err.Error())
					internalServerError(w, r)
					return
				}
				http.Redirect(w, r, r.URL.String(), http.StatusFound)
				return
			}
			http.Redirect(w, r, nbrew.Scheme+nbrew.AdminDomain+"/"+path.Join("admin", sitePrefix, response.ParentFolder, response.Name), http.StatusFound)
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
				internalServerError(w, r)
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

		response := Response{
			ParentFolder: request.ParentFolder,
			Name:         request.Name,
			Errors:       make(url.Values),
		}
		if response.ParentFolder != "" {
			response.ParentFolder = strings.Trim(path.Clean(response.ParentFolder), "/")
		}

		head, tail, _ := strings.Cut(response.ParentFolder, "/")
		if head != "posts" && head != "notes" && head != "pages" && head != "themes" {
			response.Errors.Add("parent_folder", "parent folder has to start with posts, notes, pages or themes")
		} else if (head == "posts" || head == "notes") && strings.Contains(tail, "/") {
			response.Errors.Add("parent_folder", "not allowed to use this parent folder")
		}
		if (head == "posts" || head == "notes") && response.Name == "" {
			response.Name = strings.ToLower(ulid.Make().String()) + ".md"
		}
		if response.Name == "" {
			response.Errors.Add("name", "cannot be empty")
		} else {
			errmsgs := validateName(response.Name)
			if len(errmsgs) > 0 {
				response.Errors["name"] = append(response.Errors["name"], errmsgs...)
			}
			switch head {
			case "posts", "notes":
				if path.Ext(response.Name) != ".md" {
					response.Errors.Add("name", "invalid extension (must end in .md)")
				}
			case "pages":
				if path.Ext(response.Name) != ".html" {
					response.Errors.Add("name", "invalid extension (must end in .html)")
				}
			case "themes":
				ext := path.Ext(response.Name)
				if ext == ".gz" {
					ext = path.Ext(strings.TrimSuffix(response.Name, ext))
				}
				allowedExts := []string{
					".html", ".css", ".js", ".md", ".txt",
					".jpeg", ".jpg", ".png", ".gif", ".svg", ".ico",
					".eof", ".ttf", ".woff", ".woff2",
					".csv", ".tsv", ".json", ".xml", ".toml", ".yaml", ".yml",
				}
				if !slices.Contains(allowedExts, ext) {
					response.Errors.Add("name", fmt.Sprintf("invalid extension (must be one of: %s)", strings.Join(allowedExts, ", ")))
				}
			}
		}
		if len(response.Errors) > 0 {
			writeResponse(w, r, response)
			return
		}

		_, err := fs.Stat(nbrew.FS, path.Join(sitePrefix, response.ParentFolder))
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				response.Errors.Add("parent_folder", "folder does not exist")
				writeResponse(w, r, response)
				return
			}
			logger.Error(err.Error())
			internalServerError(w, r)
			return
		}

		fileInfo, err := fs.Stat(nbrew.FS, path.Join(sitePrefix, response.ParentFolder, response.Name))
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			logger.Error(err.Error())
			internalServerError(w, r)
			return
		}
		if err == nil {
			if fileInfo.IsDir() {
				response.Errors.Add("name", "folder with the same name already exists")
			} else {
				response.AlreadyExists = "/" + path.Join("admin", sitePrefix, response.ParentFolder, response.Name)
			}
			writeResponse(w, r, response)
			return
		}

		readerFrom, err := nbrew.FS.OpenReaderFrom(path.Join(sitePrefix, response.ParentFolder, response.Name), 0644)
		if err != nil {
			logger.Error(err.Error())
			internalServerError(w, r)
			return
		}
		_, err = readerFrom.ReadFrom(bytes.NewReader(nil))
		if err != nil {
			logger.Error(err.Error())
			internalServerError(w, r)
			return
		}
		writeResponse(w, r, response)
	default:
		http.Error(w, "405 Method Not Allowed", http.StatusMethodNotAllowed)
	}
}
