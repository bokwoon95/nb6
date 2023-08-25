package nb6

import (
	"bytes"
	"html/template"
	"net/http"
	"net/url"
	"path"
	"strings"

	"golang.org/x/exp/slog"
)

func (nbrew *Notebrew) createNote(w http.ResponseWriter, r *http.Request, username string) {
	type Request struct {
		Category string `json:"category,omitempty"`
		Content  string `json:"content,omitempty"`
	}
	type Response struct {
		Request `json:"request"`
		NoteID  string     `json:"note_id,omitempty"`
		Errors  url.Values `json:"errors,omitempty"`
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
		var response Response
		ok, err := nbrew.getSession(r, "flash", &response)
		if err != nil {
			logger.Error(err.Error())
		} else if !ok {
			response.Request.Category = r.Form.Get("category")
		}
		nbrew.clearSession(w, r, "flash")

		dirEntries, err := nbrew.FS.ReadDir(path.Join(sitePrefix, "notes"))
		if err != nil {
			logger.Error(err.Error())
			http.Error(w, messageInternalServerError, http.StatusInternalServerError)
			return
		}
		var categories []string
		for _, dirEntry := range dirEntries {
			if dirEntry.IsDir() {
				categories = append(categories, dirEntry.Name())
			}
		}

		text, err := readFile(rootFS, "html/create_note.html")
		if err != nil {
			logger.Error(err.Error())
			http.Error(w, messageInternalServerError, http.StatusInternalServerError)
			return
		}
		funcMap := map[string]any{
			"siteURL": nbrew.siteURL(sitePrefix),
			"username": func() string {
				if username == "" {
					return "user"
				}
				return "@" + username
			},
			"categories": func() []string { return categories },
		}
		tmpl, err := template.New("").Funcs(funcMap).Parse(text)
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
	default:
		http.Error(w, "405 Method Not Allowed", http.StatusMethodNotAllowed)
	}
}
