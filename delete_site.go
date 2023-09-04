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

	"github.com/bokwoon95/sq"
	"golang.org/x/exp/slog"
)

func (nbrew *Notebrew) deleteSite(w http.ResponseWriter, r *http.Request, username string) {
	type Request struct {
		SiteName string `json:"site_name,omitempty"`
	}
	type Response struct {
		Errors []string `json:"errors,omitempty"`
	}
	type TemplateData struct {
		SiteName   string `json:"site_name,omitempty"`
		SitePrefix string `json:"site_prefix,omitempty"`
		Referer    string `json:"referer,omitempty"`
	}

	logger, ok := r.Context().Value(loggerKey).(*slog.Logger)
	if !ok {
		logger = slog.Default()
	}

	toSitePrefix := func(siteName string) (sitePrefix string, ok bool) {
		for _, char := range siteName {
			if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '-' || char == '.' {
				continue
			}
			return "", false
		}
		if len(siteName) > 30 {
			return "", false
		}
		if nbrew.DB != nil {
			exists, err := sq.FetchExistsContext(r.Context(), nbrew.DB, sq.CustomQuery{
				Dialect: nbrew.Dialect,
				Format: "SELECT 1" +
					" FROM site" +
					" JOIN site_user ON site_user.site_id = site.site_id" +
					" JOIN users ON users.user_id = site_user.user_id" +
					" WHERE site.site_name = {siteName}" +
					" AND users.username = {username}",
				Values: []any{
					sq.StringParam("siteName", siteName),
					sq.StringParam("username", username),
				},
			})
			if err != nil {
				logger.Error(err.Error())
			}
			if !exists {
				return "", false
			}
		}
		sitePrefix = siteName
		if !strings.Contains(sitePrefix, ".") {
			sitePrefix = "@" + sitePrefix
		}
		return sitePrefix, true
	}

	switch r.Method {
	case "GET":
		err := r.ParseForm()
		if err != nil {
			http.Error(w, fmt.Sprintf("400 Bad Request: %s", err), http.StatusBadRequest)
			return
		}
		request := Request{
			SiteName: r.Form.Get("site_name"),
		}
		var templateData TemplateData
		sitePrefix, ok := toSitePrefix(request.SiteName)
		if ok {
			templateData.SiteName = request.SiteName
			templateData.SitePrefix = sitePrefix
		}

		tmpl, err := template.New("delete_site.html").ParseFS(rootFS, "html/delete_site.html")
		if err != nil {
			logger.Error(err.Error())
			internalServerError(w, r)
			return
		}
		buf := bufPool.Get().(*bytes.Buffer)
		buf.Reset()
		defer bufPool.Put(buf)
		err = tmpl.Execute(buf, &templateData)
		if err != nil {
			logger.Error(err.Error())
			internalServerError(w, r)
			return
		}
		w.Header().Add("Content-Security-Policy", defaultContentSecurityPolicy)
		buf.WriteTo(w)
	case "POST":
		writeResponse := func(w http.ResponseWriter, r *http.Request, response Response, sitePrefix string) {
			accept, _, _ := mime.ParseMediaType(r.Header.Get("Accept"))
			if accept == "application/json" {
				w.Header().Set("Content-Type", "application/json")
				b, err := json.Marshal(&response)
				if err != nil {
					logger.Error(err.Error())
					internalServerError(w, r)
					return
				}
				w.Write(b)
				return
			}
			if len(response.Errors) == 0 {
				err := nbrew.setSession(w, r, "flash", map[string]any{
					"alerts": url.Values{
						"success": []string{"site deleted: " + sitePrefix},
					},
				})
				if err != nil {
					logger.Error(err.Error())
				}
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
				internalServerError(w, r)
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

		response := Response{}
		sitePrefix, ok := toSitePrefix(request.SiteName)
		if !ok {
			response.Errors = append(response.Errors, "site doesn't exist or you don't have permission to delete the site")
			writeResponse(w, r, response, sitePrefix)
			return
		}

		err := removeAll(nbrew.FS, sitePrefix)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			logger.Error(err.Error())
			internalServerError(w, r)
			return
		}

		if nbrew.DB != nil {
			tx, err := nbrew.DB.Begin()
			if err != nil {
				logger.Error(err.Error())
				internalServerError(w, r)
				return
			}
			defer tx.Rollback()
			_, err = sq.ExecContext(r.Context(), tx, sq.CustomQuery{
				Dialect: nbrew.Dialect,
				Format: "DELETE FROM site_user" +
					" WHERE EXISTS (" +
					"SELECT 1 FROM site WHERE site.site_id = site_user.site_id AND site.site_name = {siteName}" +
					")",
				Values: []any{
					sq.StringParam("siteName", request.SiteName),
				},
			})
			if err != nil {
				logger.Error(err.Error())
				internalServerError(w, r)
				return
			}
			_, err = sq.ExecContext(r.Context(), tx, sq.CustomQuery{
				Dialect: nbrew.Dialect,
				Format:  "DELETE FROM site WHERE site_name = {siteName}",
				Values: []any{
					sq.StringParam("siteName", request.SiteName),
				},
			})
			if err != nil {
				logger.Error(err.Error())
				internalServerError(w, r)
				return
			}
			err = tx.Commit()
			if err != nil {
				logger.Error(err.Error())
				internalServerError(w, r)
				return
			}
		}

		writeResponse(w, r, response, sitePrefix)
	default:
		http.Error(w, "405 Method Not Allowed", http.StatusMethodNotAllowed)
	}
}
