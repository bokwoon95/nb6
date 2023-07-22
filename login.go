package nb6

import (
	"fmt"
	"net/http"

	"github.com/bokwoon95/sq"
	"golang.org/x/exp/slog"
)

func (nbrew *Notebrew) login(w http.ResponseWriter, r *http.Request) {
	logger, ok := r.Context().Value(loggerKey).(*slog.Logger)
	if !ok {
		logger = slog.Default()
	}
	if nbrew.DB == nil {
		http.Error(w, "404 Not Found", http.StatusNotFound)
		return
	}
		err := r.ParseForm()
		if err != nil {
			http.Error(w, fmt.Sprintf("invalid request query or body: %v", err), http.StatusBadRequest)
			return
		}
	switch r.Method {
	case "GET":
		authenticationTokenHash := getAuthenticationTokenHash(r)
		if authenticationTokenHash != nil {
			exists, err := sq.FetchExistsContext(r.Context(), nbrew.DB, sq.CustomQuery{
				Dialect: nbrew.Dialect,
				Format:  "SELECT 1 FROM authentications WHERE authentication_token_hash = {authenticationTokenHash}",
				Values: []any{
					sq.BytesParam("authenticationTokenHash", authenticationTokenHash),
				},
			})
			if err != nil {
				logger.Error(err.Error())
				http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
				return
			}
			if exists {
				http.Redirect(w, r, nbrew.Scheme+nbrew.AdminDomain+"/admin/", http.StatusFound)
				return
			}
		}
	case "POST":
	}
}
