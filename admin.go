package nb6

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/bokwoon95/sq"
	"golang.org/x/exp/slog"
)

func (nbrew *Notebrew) admin(w http.ResponseWriter, r *http.Request) {
	logger, ok := r.Context().Value(loggerKey).(*slog.Logger)
	if !ok {
		logger = slog.Default()
	}

	segments := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(segments) <= 1 {
		http.Error(w, "404 Not Found", http.StatusNotFound)
		return
	}

	action := segments[1]
	if action == "static" || action == "login" || action == "logout" || action == "resetpassword" {
		if len(segments) > 2 {
			http.Error(w, "404 Not Found", http.StatusNotFound)
			return
		}
		switch action {
		case "static":
			nbrew.static(w, r)
		case "login":
			nbrew.login(w, r)
		case "logout":
			nbrew.logout(w, r)
		case "resetpassword":
			nbrew.resetpassword(w, r)
		}
		return
	}

	var siteName string
	if strings.HasPrefix(action, "@") || strings.Contains(action, ".") {
		if len(segments) < 3 {
			http.Error(w, "404 Not Found", http.StatusNotFound)
			return
		}
		siteName, action = strings.TrimPrefix(action, "@"), segments[2]
	}

	if nbrew.DB != nil {
		authenticationTokenHash := getAuthenticationTokenHash(r)
		if authenticationTokenHash == nil {
			http.Redirect(w, r, "/admin/login/", http.StatusFound)
			return
		}
		username, err := sq.FetchOneContext(r.Context(), nbrew.DB, sq.CustomQuery{
			Dialect: nbrew.Dialect,
			Format: "SELECT {*}" +
				" FROM site_user" +
				" JOIN authentication ON authentication.user_id = site_user.user_id" +
				" JOIN users ON users.user_id = site_user.user_id" +
				" WHERE site_user.site_name = {siteName}" +
				" AND authentication.authentication_token_hash = {authenticationTokenHash}",
			Values: []any{
				sq.StringParam("siteName", siteName),
				sq.BytesParam("authenticationTokenHash", authenticationTokenHash),
			},
		}, func(row *sq.Row) string {
			return row.String("users.username")
		})
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				http.Error(w, "Forbidden", http.StatusForbidden)
				return
			}
			logger.Error(err.Error())
			http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
			return
		}
		r = r.WithContext(context.WithValue(r.Context(), loggerKey, logger.With(
			slog.String("username", username),
		)))
	}

	switch action {
	case "posts", "notes", "pages", "templates", "assets":
		nbrew.file(w, r)
	case "recyclebin":
		nbrew.recyclebin(w, r)
	case "createfile":
		nbrew.createfile(w, r)
	case "createfolder":
		nbrew.createfolder(w, r)
	case "rename":
		nbrew.rename(w, r)
	case "delete":
		nbrew.delet(w, r)
	case "move":
		nbrew.move(w, r)
	case "copy":
		nbrew.cpy(w, r)
	default:
		http.Error(w, "404 Not Found", http.StatusNotFound)
	}

}
