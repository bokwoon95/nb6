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

	"github.com/bokwoon95/sq"
	"golang.org/x/exp/slog"
)

func (nbrew *Notebrew) createSite(w http.ResponseWriter, r *http.Request, username string) {
	type Request struct {
		SiteName string `json:"site_name,omitempty"`
	}
	type Response struct {
		SiteName string     `json:"site_name,omitempty"`
		Errors   url.Values `json:"errors,omitempty"`
	}

	logger, ok := r.Context().Value(loggerKey).(*slog.Logger)
	if !ok {
		logger = slog.Default()
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
			"username": func() string { return username },
			"referer":  func() string { return r.Referer() },
			"safeHTML": func(s string) template.HTML { return template.HTML(s) },
		}
		tmpl, err := template.New("create_site.html").Funcs(funcMap).ParseFS(rootFS, "create_site.html")
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
			sitePrefix := response.SiteName
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
				internalServerError(w, r, err)
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
				internalServerError(w, r, err)
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
			SiteName: request.SiteName,
			Errors:   make(url.Values),
		}
		if request.SiteName == "" {
			response.Errors.Add("site_name", "cannot be blank")
			writeResponse(w, r, response)
			return
		}
		for _, char := range request.SiteName {
			if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '-' || char == '.' {
				continue
			}
			response.Errors.Add("site_name", "forbidden characters - only lowercase letters, numbers and hyphen are allowed")
			break
		}
		if len(request.SiteName) > 30 {
			response.Errors.Add("site_name", "length cannot exceed 30 characters")
		}
		if len(response.Errors) > 0 {
			writeResponse(w, r, response)
			return
		}
		sitePrefix := request.SiteName
		if !strings.Contains(sitePrefix, ".") {
			sitePrefix = "@" + sitePrefix
		}
		fileInfo, err := fs.Stat(nbrew.FS, sitePrefix)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			logger.Error(err.Error())
			internalServerError(w, r, err)
			return
		}
		if fileInfo != nil {
			response.Errors.Add("site_name", "name is unavailable")
			writeResponse(w, r, response)
			return
		}

		err = nbrew.FS.Mkdir(sitePrefix, 0755)
		if err != nil && !errors.Is(err, fs.ErrExist) {
			logger.Error(err.Error())
			internalServerError(w, r, err)
			return
		}
		dirs := []string{
			"notes",
			"pages",
			"posts",
			"site",
			"site/images",
			"site/themes",
			"system",
		}
		for _, dir := range dirs {
			err = nbrew.FS.Mkdir(path.Join(sitePrefix, dir), 0755)
			if err != nil && !errors.Is(err, fs.ErrExist) {
				logger.Error(err.Error())
				internalServerError(w, r, err)
				return
			}
		}
		if nbrew.DB != nil {
			tx, err := nbrew.DB.Begin()
			if err != nil {
				logger.Error(err.Error())
				internalServerError(w, r, err)
				return
			}
			defer tx.Rollback()
			siteID := NewID()
			_, err = sq.ExecContext(r.Context(), tx, sq.CustomQuery{
				Dialect: nbrew.Dialect,
				Format: "INSERT INTO site (site_id, site_name)" +
					" VALUES ({siteID}, {siteName}) {conflictClause}",
				Values: []any{
					sq.UUIDParam("siteID", siteID),
					sq.StringParam("siteName", request.SiteName),
					sq.Param("conflictClause", sq.DialectExpression{
						Default: sq.Expr("ON CONFLICT DO NOTHING"),
						Cases: []sq.DialectCase{{
							Dialect: sq.DialectMySQL,
							Result:  sq.Expr("ON DUPLICATE KEY UPDATE site_id = site_id"),
						}},
					}),
				},
			})
			if err != nil {
				logger.Error(err.Error())
				internalServerError(w, r, err)
				return
			}
			_, err = sq.ExecContext(r.Context(), tx, sq.CustomQuery{
				Dialect: nbrew.Dialect,
				Format: "INSERT INTO site_user (site_id, user_id)" +
					" VALUES ((SELECT site_id FROM site WHERE site_name = {siteName}), (SELECT user_id FROM users WHERE username = {username})) {conflictClause}",
				Values: []any{
					sq.StringParam("siteName", request.SiteName),
					sq.StringParam("username", username),
					sq.Param("conflictClause", sq.DialectExpression{
						Default: sq.Expr("ON CONFLICT DO NOTHING"),
						Cases: []sq.DialectCase{{
							Dialect: sq.DialectMySQL,
							Result:  sq.Expr("ON DUPLICATE KEY UPDATE site_id = site_id"),
						}},
					}),
				},
			})
			if err != nil {
				logger.Error(err.Error())
				internalServerError(w, r, err)
				return
			}
			err = tx.Commit()
			if err != nil {
				logger.Error(err.Error())
				internalServerError(w, r, err)
				return
			}
		}

		writeResponse(w, r, response)
	default:
		http.Error(w, "405 Method Not Allowed", http.StatusMethodNotAllowed)
	}
}
