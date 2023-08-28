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
			"siteURL":  nbrew.siteURL(sitePrefix),
			"username": func() string { return username },
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
		writeResponse := func(w http.ResponseWriter, r *http.Request, response Response) {
			accept, _, _ := mime.ParseMediaType(r.Header.Get("Accept"))
			if accept == "application/json" {
				b, err := json.Marshal(&response)
				if err != nil {
					logger.Error(err.Error())
					http.Error(w, messageInternalServerError, http.StatusInternalServerError)
					return
				}
				w.Write(b)
				return
			}
			if len(response.Errors) > 0 {
				err := nbrew.setSession(w, r, "flash", &response)
				if err != nil {
					logger.Error(err.Error())
					http.Error(w, messageInternalServerError, http.StatusInternalServerError)
					return
				}
				http.Redirect(w, r, r.URL.String(), http.StatusFound)
				return
			}
			sitePrefix := response.Request.SiteName
			if !strings.Contains(sitePrefix, ".") {
				sitePrefix = "@" + sitePrefix
			}
			err := nbrew.setSession(w, r, "flash", map[string]any{
				"alerts": url.Values{
					"success": []string{
						fmt.Sprintf(`Site created: <a href="/admin/%[1]s/" class="linktext">%[1]s</a>`, sitePrefix),
					},
				},
			})
			if err != nil {
				logger.Error(err.Error())
				http.Error(w, messageInternalServerError, http.StatusInternalServerError)
				return
			}
			http.Redirect(w, r, nbrew.Scheme+nbrew.AdminDomain+"/admin/", http.StatusFound)
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
			request.SiteName = r.Form.Get("site_name")
		default:
			http.Error(w, "415 Unsupported Media Type", http.StatusUnsupportedMediaType)
			return
		}

		response := Response{
			Request: request,
			Errors:  make(url.Values),
		}
		if request.SiteName == "" {
			response.Errors.Add("site_name", "cannot be blank")
			writeResponse(w, r, response)
			return
		}
		for _, char := range request.SiteName {
			if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '_' {
				continue
			}
			response.Errors.Add("site_name", "forbidden characters present - only lowercase letters, numbers and hyphen are allowed")
		}
		if len(request.SiteName) > 30 {
			response.Errors.Add("site_name", "length cannot exceed 30 characters")
		}
		if len(response.Errors) > 0 {
			writeResponse(w, r, response)
			return
		}
		fileInfo, err := fs.Stat(nbrew.FS, sitePrefix)

		// forbidden characters present - only lowercase letters, numbers and hyphen are allowed
		// length cannot exceed 30 characters
		// name already taken
		writeResponse(w, r, response)
	default:
		http.Error(w, "405 Method Not Allowed", http.StatusMethodNotAllowed)
	}
}
