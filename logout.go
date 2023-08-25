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
	authenticationTokenHash := getAuthenticationTokenHash(r)
	if authenticationTokenHash == nil {
		http.Redirect(w, r, nbrew.Protocol+nbrew.AdminDomain+"/admin/", http.StatusFound)
		return
	}
	switch r.Method {
	case "GET":
		text, err := readFile(rootFS, "html/logout.html")
		if err != nil {
			logger.Error(err.Error())
			http.Error(w, messageInternalServerError, http.StatusInternalServerError)
			return
		}
		tmpl, err := template.New("").Parse(text)
		if err != nil {
			logger.Error(err.Error())
			http.Error(w, messageInternalServerError, http.StatusInternalServerError)
			return
		}
		buf := bufPool.Get().(*bytes.Buffer)
		buf.Reset()
		defer bufPool.Put(buf)
		err = tmpl.Execute(buf, nil)
		if err != nil {
			logger.Error(err.Error())
			http.Error(w, messageInternalServerError, http.StatusInternalServerError)
			return
		}
		buf.WriteTo(w)
	case "POST":
		http.SetCookie(w, &http.Cookie{
			Path:   "/",
			Name:   "authentication",
			Value:  "",
			MaxAge: -1,
		})
		_, err := sq.ExecContext(r.Context(), nbrew.DB, sq.CustomQuery{
			Dialect: nbrew.Dialect,
			Format:  "DELETE FROM authentication WHERE authentication_token_hash = {authenticationTokenHash}",
			Values: []any{
				sq.BytesParam("authenticationTokenHash", authenticationTokenHash),
			},
		})
		if err != nil {
			logger.Error(err.Error())
			http.Error(w, messageInternalServerError, http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, nbrew.Protocol+nbrew.AdminDomain+"/admin/", http.StatusFound)
	default:
		http.Error(w, "405 Method Not Allowed", http.StatusMethodNotAllowed)
	}
}
