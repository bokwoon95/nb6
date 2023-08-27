package nb6

import (
	"bytes"
	"html/template"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/exp/slog"
)

func (nbrew *Notebrew) createSite(w http.ResponseWriter, r *http.Request, username string) {
	type Request struct {
		SiteName string `json:"site_name,omitempty"`
	}
	type Response struct {
		Request Request    `json:"request"`
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
		_, err := nbrew.getSession(r, "flash", &response)
		if err != nil {
			logger.Error(err.Error())
		}
		nbrew.clearSession(w, r, "flash")

		funcMap := map[string]any{
			"siteURL": nbrew.siteURL(sitePrefix),
			"username": func() string {
				if username == "" {
					return "user"
				}
				return "@" + username
			},
		}
		tmpl, err := template.New("create_site.html").Funcs(funcMap).ParseFS(rootFS, "html/create_site.html")
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
