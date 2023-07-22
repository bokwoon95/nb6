package nb6

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"mime"
	"net/http"
	"net/url"
	"strings"

	"github.com/bokwoon95/sq"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/exp/slog"
)

func (nbrew *Notebrew) login(w http.ResponseWriter, r *http.Request) {
	type Request struct {
		Email    string `json:"email,omitempty"`
		Password string `json:"password,omitempty"`
		Referer  string `json:"referer,omitempty"`
	}
	type Response struct {
		Email         string `json:"email,omitempty"`
		Password      string `json:"password,omitempty"`
		Referer       string `json:"referer,omitempty"`
		Error         string `json:"error,omitempty"`
		PasswordReset bool   `json:"password_reset,omitempty"`
	}

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
			} else if exists {
				http.Redirect(w, r, nbrew.Scheme+nbrew.AdminDomain+"/admin/", http.StatusFound)
				return
			}
		}
		var response Response
		ok, err := nbrew.getSession(r, "flash", &response)
		if err != nil {
			logger.Error(err.Error())
		} else if !ok {
			response.Referer = r.Form.Get("referer")
		}
		buf := bufPool.Get().(*bytes.Buffer)
		buf.Reset()
		defer bufPool.Put(buf)
		tmpl, err := template.ParseFS(rootFS, "html/login.html")
		if err != nil {
			logger.Error(err.Error())
			http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
			return
		}
		err = tmpl.Execute(buf, &response)
		if err != nil {
			logger.Error(err.Error())
			http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
			return
		}
		buf.WriteTo(w)
	case "POST":
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
				http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
				return
			}
		case "application/x-www-form-urlencoded":
			err := r.ParseForm()
			if err != nil {
				http.Error(w, fmt.Sprintf("400 Bad Request: %s", err), http.StatusBadRequest)
				return
			}
			request.Email = r.Form.Get("email")
			request.Password = r.Form.Get("password")
			request.Referer = r.Form.Get("referer")
		default:
			http.Error(w, "415 Unsupported Media Type", http.StatusUnsupportedMediaType)
			return
		}

		writeResponse := func(w http.ResponseWriter, r *http.Request, response Response) {
			accept, _, _ := mime.ParseMediaType(r.Header.Get("Accept"))
			if accept == "application/json" {
				b, err := json.Marshal(&response)
				if err != nil {
					logger.Error(err.Error())
					http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
					return
				}
				w.Write(b)
				return
			}
			if response.Error == "" {
				http.Redirect(w, r, "/admin/", http.StatusFound)
				return
			}
			err := nbrew.setSession(w, r, &response, &http.Cookie{
				Path:     r.URL.Path,
				Name:     "flash",
				Secure:   nbrew.Scheme == "https://",
				HttpOnly: true,
				SameSite: http.SameSiteLaxMode,
			})
			if err != nil {
				logger.Error(err.Error())
				http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
				return
			}
			http.Redirect(w, r, r.URL.String(), http.StatusFound)
		}
		_ = writeResponse

		response := Response{
			Email:    request.Email,
			Password: request.Password,
			Referer:  request.Referer,
		}
		passwordHash, err := sq.FetchOneContext(r.Context(), nbrew.DB, sq.CustomQuery{
			Dialect: nbrew.Dialect,
			Format:  "SELECT {*} FROM users WHERE email = {email}",
			Values: []any{
				sq.StringParam("email", response.Email),
			},
		}, func(row *sq.Row) []byte {
			return row.Bytes("password_hash")
		})
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			logger.Error(err.Error())
			http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
			return
		}
		err = bcrypt.CompareHashAndPassword(passwordHash, []byte(response.Password))
		if err != nil {
			response.Error = "incorrect email or password"
			return
			http.SetCookie(w, &http.Cookie{
				Path:     "/admin/login/",
				Name:     "responseCode",
				Value:    "1",
				Secure:   nbrew.Scheme == "https://",
				HttpOnly: true,
				SameSite: http.SameSiteLaxMode,
			})
			if referer != "" {
				queryParams := make(url.Values)
				queryParams.Set("referer", referer)
				http.Redirect(w, r, nbrew.AdminURL+"/admin/login/?"+queryParams.Encode(), http.StatusFound)
				return
			}
			http.Redirect(w, r, nbrew.AdminURL+"/admin/login/", http.StatusFound)
			return
		}
	}
}

func getAdminSitePrefix(urlPath string) string {
	// E.g. /admin/@siteprefix/foo/bar/baz
	head, tail, _ := strings.Cut(strings.Trim(urlPath, "/"), "/")
	if head != "admin" {
		return ""
	}
	head, _, _ = strings.Cut(strings.Trim(tail, "/"), "/")
	if !strings.HasPrefix(head, "@") && !strings.Contains(head, ".") {
		return ""
	}
	return head
}
