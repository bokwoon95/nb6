package nb6

import (
	"net/http"

	"github.com/bokwoon95/sq"
	"golang.org/x/exp/slog"
)

func (nbrew *Notebrew) logout(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "405 Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	logger, ok := r.Context().Value(loggerKey).(*slog.Logger)
	if !ok {
		logger = slog.Default()
	}
	http.SetCookie(w, &http.Cookie{
		Path:   "/",
		Name:   "authentication",
		Value:  "",
		MaxAge: -1,
	})
	authenticationTokenHash := getAuthenticationTokenHash(r)
	if authenticationTokenHash == nil {
		http.Redirect(w, r, nbrew.Protocol+nbrew.AdminDomain+"/admin/", http.StatusFound)
		return
	}
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
}
