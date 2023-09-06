package nb6

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"golang.org/x/exp/slog"
)

func (nbrew *Notebrew) createNote(w http.ResponseWriter, r *http.Request, username, sitePrefix string) {
	type Request struct {
		Slug     string `json:"slug,omitempty"`
		Category string `json:"category,omitempty"`
		Content  string `json:"content,omitempty"`
	}
	type Response struct {
		Slug     string     `json:"slug,omitempty"`
		Category string     `json:"category,omitempty"`
		Content  string     `json:"content,omitempty"`
		NoteID   string     `json:"note_id,omitempty"`
		Errors   url.Values `json:"errors,omitempty"`
	}

	logger, ok := r.Context().Value(loggerKey).(*slog.Logger)
	if !ok {
		logger = slog.Default()
	}

	switch r.Method {
	case "GET":
		var response Response
		ok, err := nbrew.getSession(r, "flash", &response)
		if err != nil {
			logger.Error(err.Error())
		} else if !ok {
			response.Category = r.Form.Get("category")
		}
		nbrew.clearSession(w, r, "flash")

		dirEntries, err := nbrew.FS.ReadDir(path.Join(sitePrefix, "notes"))
		if err != nil {
			logger.Error(err.Error())
			internalServerError(w, r, err)
			return
		}
		var categories []string
		for _, dirEntry := range dirEntries {
			if dirEntry.IsDir() {
				categories = append(categories, dirEntry.Name())
			}
		}

		funcMap := map[string]any{
			"join":       path.Join,
			"username":   func() string { return username },
			"referer":    func() string { return r.Referer() },
			"categories": func() []string { return categories },
			"sitePrefix": func() string { return sitePrefix },
		}
		tmpl, err := template.New("create_note.html").Funcs(funcMap).ParseFS(rootFS, "create_note.html")
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
			http.Redirect(w, r, nbrew.Scheme+nbrew.AdminDomain+"/"+path.Join("admin", sitePrefix, "notes", response.Category, response.NoteID)+".md", http.StatusFound)
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
			request.Slug = r.Form.Get("slug")
			request.Category = r.Form.Get("category")
			request.Content = r.Form.Get("content")
		default:
			http.Error(w, "415 Unsupported Media Type", http.StatusUnsupportedMediaType)
			return
		}

		var timestamp [8]byte
		binary.BigEndian.PutUint64(timestamp[:], uint64(time.Now().Unix()))
		noteID := base32Encoding.EncodeToString(timestamp[len(timestamp)-5:])
		slug := strings.TrimSpace(request.Slug)
		if slug == "" {
			slug, _ = getTitleAndPreview(io.NopCloser(strings.NewReader(request.Content)))
		}
		if slug != "" {
			noteID += "-" + toSlug(slug)
		}
		response := Response{
			Content: request.Content,
			NoteID:  noteID,
			Errors:  make(url.Values),
		}

		if request.Category != "" {
			fileInfo, err := fs.Stat(nbrew.FS, path.Join(sitePrefix, "notes", request.Category))
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					response.Errors.Add("category", "category does not exist")
					writeResponse(w, r, response)
					return
				}
				logger.Error(err.Error())
				internalServerError(w, r, err)
				return
			}
			if !fileInfo.IsDir() {
				response.Errors.Add("category", "category does not exist")
				writeResponse(w, r, response)
				return
			}
		}
		response.Category = request.Category

		readerFrom, err := nbrew.FS.OpenReaderFrom(path.Join(sitePrefix, "notes", response.Category, response.NoteID+".md"), 0644)
		if err != nil {
			logger.Error(err.Error())
			internalServerError(w, r, err)
			return
		}
		_, err = readerFrom.ReadFrom(strings.NewReader(response.Content))
		if err != nil {
			logger.Error(err.Error())
			internalServerError(w, r, err)
			return
		}
		writeResponse(w, r, response)
	default:
		http.Error(w, "405 Method Not Allowed", http.StatusMethodNotAllowed)
	}
}
