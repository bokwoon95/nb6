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
	segments := strings.Split(urlPath, "/")
	if len(segments) > 0 {
		if segments[0] == "static" {
			nbrew.static(w, r, urlPath)
			return
		}
		if segments[0] == "login" || segments[0] == "logout" || segments[0] == "reset_password" {
			if len(segments) > 1 {
				http.Error(w, "404 Not Found", http.StatusNotFound)
				return
			}
			switch segments[0] {
			case "login":
				nbrew.login(w, r)
			case "logout":
				nbrew.logout(w, r)
			case "reset_password":
				nbrew.resetPassword(w, r)
			}
			return
		}
	}

	var sitePrefix, resource string
	if len(segments) > 0 {
		if strings.HasPrefix(segments[0], "@") || strings.Contains(segments[0], ".") {
			sitePrefix = segments[0]
			if len(segments) > 1 {
				resource = segments[1]
			}
		} else {
			resource = segments[0]
		}
	}

	var username string
	if nbrew.DB != nil {
		authenticationTokenHash := getAuthenticationTokenHash(r)
		if authenticationTokenHash == nil {
			// TODO: make this an error page instead of an immediate redirect.
			http.Redirect(w, r, "/admin/login/", http.StatusFound)
			return
		}
		result, err := sq.FetchOneContext(r.Context(), nbrew.DB, sq.CustomQuery{
			Dialect: nbrew.Dialect,
			Format: "SELECT {*}" +
				" FROM authentication" +
				" JOIN site_user ON site_user.user_id = authentication.user_id" +
				" JOIN users ON users.user_id = site_user.user_id" +
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
				// TODO: make this an error page instead of an immediate redirect.
				http.Redirect(w, r, "/admin/login/", http.StatusFound)
				return
			}
			logger.Error(err.Error())
			http.Error(w, messageInternalServerError, http.StatusInternalServerError)
			return
		}
		if !result.IsAuthorized {
			// TODO: show error page telling the user they're not authorized.
		}
		username = result.Username
		r = r.WithContext(context.WithValue(r.Context(), loggerKey, logger.With(
			slog.String("username", username),
		)))
	}

	if resource == "" || resource == "notes" || resource == "pages" || resource == "posts" || resource == "site" {
		nbrew.filesystem(w, r, username)
		return
	}

	// Need to make sure there's nothing after the resource, but I don't know
	// how else to express it bc I was stumped when reading this piece of code
	// that I wrote (before I remembered its purpose).
	if (sitePrefix == "" && len(segments) > 1) || (sitePrefix != "" && len(segments) > 2) {
		http.Error(w, "404 Not Found", http.StatusNotFound)
		return
	}

	switch resource {
	case "create_site":
		nbrew.createSite(w, r, username)
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
		nbrew.delet(w, r)
	case "recycle_bin":
		nbrew.recycleBin(w, r)
	default:
		http.Error(w, "404 Not Found", http.StatusNotFound)
	}
}
