package nb6

import (
	"bytes"
	"html/template"
	"net/http"

	"github.com/bokwoon95/sq"
	"golang.org/x/exp/slog"
)

func (nbrew *Notebrew) logout(w http.ResponseWriter, r *http.Request) {
	logger, ok := r.Context().Value(loggerKey).(*slog.Logger)
	if !ok {
		logger = slog.Default()
	}
	if nbrew.DB == nil {
		notFound(w, r)
		return
	}
	authenticationTokenHash := getAuthenticationTokenHash(r)
	if authenticationTokenHash == nil {
		http.Redirect(w, r, nbrew.Scheme+nbrew.AdminDomain+"/admin/", http.StatusFound)
		return
	}
	switch r.Method {
	case "GET":
		funcMap := map[string]any{
			"referer": func() string { return r.Referer() },
		}
		tmpl, err := template.New("logout.html").Funcs(funcMap).ParseFS(rootFS, "logout.html")
		if err != nil {
			logger.Error(err.Error())
			internalServerError(w, r, err)
			return
		}
		buf := bufPool.Get().(*bytes.Buffer)
		buf.Reset()
		defer bufPool.Put(buf)
		err = tmpl.Execute(buf, nil)
		if err != nil {
			logger.Error(err.Error())
			internalServerError(w, r, err)
			return
		}
		w.Header().Add("Content-Security-Policy", defaultContentSecurityPolicy)
		buf.WriteTo(w)
	case "POST":
		http.SetCookie(w, &http.Cookie{
			Path:   "/",
			Name:   "authentication",
			Value:  "",
			MaxAge: -1,
		})
		if authenticationTokenHash != nil {
			_, err := sq.ExecContext(r.Context(), nbrew.DB, sq.CustomQuery{
				Dialect: nbrew.Dialect,
				Format:  "DELETE FROM authentication WHERE authentication_token_hash = {authenticationTokenHash}",
				Values: []any{
					sq.BytesParam("authenticationTokenHash", authenticationTokenHash),
				},
			})
			if err != nil {
				logger.Error(err.Error())
				internalServerError(w, r, err)
				return
			}
		}
		http.Redirect(w, r, nbrew.Scheme+nbrew.AdminDomain+"/admin/login/", http.StatusFound)
	default:
		http.Error(w, "405 Method Not Allowed", http.StatusMethodNotAllowed)
	}
}
