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

	urlPath := strings.Trim(strings.TrimPrefix(r.URL.Path, "/admin"), "/")
	head, tail, _ := strings.Cut(urlPath, "/")
	if head == "static" {
		nbrew.static(w, r, urlPath)
		return
	}
	if head == "login" || head == "logout" || head == "reset_password" {
		if tail != "" {
			http.Error(w, "404 Not Found", http.StatusNotFound)
			return
		}
		switch head {
		case "login":
			nbrew.login(w, r)
		case "logout":
			nbrew.logout(w, r)
		case "reset_password":
			nbrew.resetPassword(w, r)
		}
		return
	}

	var sitePrefix string
	if strings.HasPrefix(head, "@") || strings.Contains(head, ".") {
		sitePrefix, urlPath = head, tail
		head, tail, _ = strings.Cut(urlPath, "/")
	}

	var username string
	if nbrew.DB != nil {
		authenticationTokenHash := getAuthenticationTokenHash(r)
		if authenticationTokenHash == nil {
			w.WriteHeader(http.StatusUnauthorized)
			nbrew.login(w, r)
			return
		}
		result, err := sq.FetchOneContext(r.Context(), nbrew.DB, sq.CustomQuery{
			Dialect: nbrew.Dialect,
			Format: "SELECT {*}" +
				" FROM authentication" +
				" JOIN users ON users.user_id = authentication.user_id" +
				" LEFT JOIN site_user ON site_user.user_id = authentication.user_id" +
				" LEFT JOIN site ON site.site_id = site_user.site_id AND site.site_name = {siteName}" +
				" WHERE authentication.authentication_token_hash = {authenticationTokenHash}" +
				" LIMIT 1",
			Values: []any{
				sq.StringParam("siteName", strings.TrimPrefix(sitePrefix, "@")),
				sq.BytesParam("authenticationTokenHash", authenticationTokenHash),
			},
		}, func(row *sq.Row) (result struct {
			Username     string
			IsAuthorized bool
		}) {
			result.Username = row.String("users.username")
			result.IsAuthorized = row.Bool("site.site_name IS NOT NULL")
			return result
		})
		// If no rows, user is not authenticated.
		// If row but site is null, user is not authorized
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				w.WriteHeader(http.StatusUnauthorized)
				nbrew.login(w, r)
				return
			}
			logger.Error(err.Error())
			http.Error(w, messageInternalServerError, http.StatusInternalServerError)
			return
		}
		if !result.IsAuthorized {
			forbidden(w, r)
			return
		}
		username = result.Username
		r = r.WithContext(context.WithValue(r.Context(), loggerKey, logger.With(
			slog.String("username", username),
		)))
	}

	if head == "" || head == "notes" || head == "pages" || head == "posts" || head == "site" {
		nbrew.filesystem(w, r, username, sitePrefix, urlPath)
		return
	}
	if tail != "" {
		http.Error(w, "404 Not Found", http.StatusNotFound)
		return
	}
	switch head {
	case "create_site":
		nbrew.createSite(w, r, username)
	case "delete_site":
		nbrew.deleteSite(w, r, username)
	case "create_note":
		nbrew.createNote(w, r, username, sitePrefix)
	case "create_note_category":
		nbrew.createNoteCategory(w, r)
	case "create_post":
		nbrew.createPost(w, r)
	case "create_post_category":
		nbrew.createNoteCategory(w, r)
	case "create_file":
		nbrew.createFile(w, r)
	case "create_folder":
		nbrew.createFolder(w, r)
	case "cut":
		nbrew.cpy(w, r)
	case "copy":
		nbrew.cpy(w, r)
	case "paste":
		nbrew.cpy(w, r)
	case "rename":
		nbrew.rename(w, r)
	case "delete":
		nbrew.delet(w, r, username, sitePrefix)
	case "recycle_bin":
		nbrew.recycleBin(w, r)
	default:
		notFound(w, r)
	}
}
