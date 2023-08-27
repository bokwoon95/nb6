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
	logger.Info("wee")

	var prefix string
	segments := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(segments) > 1 {
		prefix = segments[1] // segment[0] is "admin"
	}

	if prefix == "static" {
		nbrew.static(w, r)
		return
	}

	if prefix == "login" || prefix == "logout" || prefix == "resetpassword" {
		if len(segments) > 2 {
			http.Error(w, "404 Not Found", http.StatusNotFound)
			return
		}
		switch prefix {
		case "login":
			nbrew.login(w, r)
		case "logout":
			nbrew.logout(w, r)
		case "resetpassword":
			nbrew.resetPassword(w, r)
		}
		return
	}

	var siteName string
	if strings.HasPrefix(prefix, "@") || strings.Contains(prefix, ".") {
		siteName = strings.TrimPrefix(prefix, "@")
		if len(segments) > 2 {
			prefix = segments[2]
		} else {
			prefix = ""
		}
	}

	var username string
	if nbrew.DB != nil {
		authenticationTokenHash := getAuthenticationTokenHash(r)
		if authenticationTokenHash == nil {
			http.Redirect(w, r, "/admin/login/", http.StatusFound)
			return
		}
		var err error
		username, err = sq.FetchOneContext(r.Context(), nbrew.DB, sq.CustomQuery{
			Dialect: nbrew.Dialect,
			Format: "SELECT {*}" +
				" FROM authentication" +
				" JOIN site_user ON site_user.user_id = authentication.user_id" +
				" JOIN site ON site.site_id = site_user.site_id" +
				" JOIN users ON users.user_id = site_user.user_id" +
				" WHERE site.site_name = {siteName}" +
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
				// TODO: we need to distinguish between user not exists and
				// user not authorized. Currently I am logged in as root but am
				// not authorized to access @bokwoon (though I should really
				// have access to all sites as root, but that's a separate
				// issue. Fix the unauthorized user seeing an unauthorized page
				// instead).
				http.Redirect(w, r, "/admin/login/", http.StatusFound)
				return
			}
			logger.Error(err.Error())
			http.Error(w, messageInternalServerError, http.StatusInternalServerError)
			return
		}
		r = r.WithContext(context.WithValue(r.Context(), loggerKey, logger.With(
			slog.String("username", username),
		)))
	}

	if prefix == "" || prefix == "notes" || prefix == "pages" || prefix == "posts" || prefix == "site" {
		nbrew.filesystem(w, r, username)
		return
	}

	if (siteName == "" && len(segments) > 2) || (siteName != "" && len(segments) > 3) {
		http.Error(w, "404 Not Found", http.StatusNotFound)
		return
	}

	switch prefix {
	case "recyclebin":
		nbrew.recyclebin(w, r)
	case "create_site":
		nbrew.createSite(w, r, username)
	case "create_note":
		nbrew.createNote(w, r, username)
	case "create_post":
		nbrew.createPost(w, r)
	case "create_file":
		nbrew.createFile(w, r)
	case "create_folder":
		nbrew.createFolder(w, r)
	case "create_note_category":
		nbrew.createNoteCategory(w, r)
	case "create_post_category":
		nbrew.createNoteCategory(w, r)
	case "cut":
		nbrew.cpy(w, r)
	case "copy":
		nbrew.cpy(w, r)
	case "paste":
		nbrew.cpy(w, r)
	case "rename":
		nbrew.rename(w, r)
	case "delete":
		nbrew.delet(w, r)
	default:
		http.Error(w, "404 Not Found", http.StatusNotFound)
	}
}
