package nb6

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"mime"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/bokwoon95/sq"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/crypto/blake2b"
	"golang.org/x/exp/slog"
)

func (nbrew *Notebrew) login(w http.ResponseWriter, r *http.Request) {
	type Request struct {
		Username string `json:"username,omitempty"`
		Password string `json:"password,omitempty"`
		Referer  string `json:"referer,omitempty"`
	}
	// TODO: convert *Errors into a general purpose Error url.Values struct instead.
	// TODO: package flatjson => flatjson.Unflatten(map[string]any) []byte => flatjson.Flatten([]byte) map[string]any
	// "$.errors[''][0]" => "lorem ipsum"
	// "$.username[0]" => "cannot be empty"
	type Response struct {
		Username            string   `json:"username,omitempty"`
		UsernameErrors      []string `json:"username_errors,omitempty"`
		Password            string   `json:"password,omitempty"`
		PasswordErrors      []string `json:"password_errors,omitempty"`
		Referer             string   `json:"referer,omitempty"`
		Error               string   `json:"error,omitempty"`
		PasswordReset       bool     `json:"password_reset,omitempty"`
		AuthenticationToken string   `json:"authentication_token,omitempty"`
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
		var response Response
		ok, err := nbrew.getSession(r, "flash", &response)
		if err != nil {
			logger.Error(err.Error())
		} else if !ok {
			response.Referer = r.Form.Get("referer")
		}
		nbrew.clearSession(w, r, "flash")
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
			request.Username = r.Form.Get("username")
			request.Password = r.Form.Get("password")
			request.Referer = r.Form.Get("referer")
		default:
			http.Error(w, "415 Unsupported Media Type", http.StatusUnsupportedMediaType)
			return
		}

		writeResponse := func(w http.ResponseWriter, r *http.Request, response Response) {
			response.PasswordReset = false
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
			if response.Error != "" {
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
				return
			}
			http.SetCookie(w, &http.Cookie{
				Path:     "/",
				Name:     "authentication",
				Value:    response.AuthenticationToken,
				Secure:   nbrew.Scheme == "https://",
				HttpOnly: true,
				SameSite: http.SameSiteLaxMode,
			})
			referer := strings.Trim(path.Clean(response.Referer), "/")
			head, tail, _ := strings.Cut(referer, "/")
			if head == "admin" && tail != "" {
				http.Redirect(w, r, nbrew.Scheme+nbrew.AdminDomain+"/"+referer+"/", http.StatusFound)
				return
			}
			http.Redirect(w, r, nbrew.Scheme+nbrew.AdminDomain+"/admin/", http.StatusFound)
		}

		response := Response{
			Username: request.Username,
			Password: request.Password,
			Referer:  request.Referer,
		}
		// if response.Username == "" {
		// }
		// var username, email string
		query := sq.CustomQuery{Dialect: nbrew.Dialect}
		if !strings.Contains(response.Username, "@") {
			query.Format = "SELECT {*} FROM users WHERE username = {username}"
			query.Values = []any{
				sq.StringParam("username", response.Username),
			}
		} else {
			if strings.HasPrefix(response.Username, "@") {
				query.Format = "SELECT {*} FROM users WHERE username = {username}"
				query.Values = []any{
					sq.StringParam("username", strings.TrimPrefix(response.Username, "@")),
				}
			} else {
				query.Format = "SELECT {*} FROM users WHERE email = {email}"
				query.Values = []any{
					sq.StringParam("email", response.Username),
				}
			}
		}
		passwordHash, err := sq.FetchOneContext(r.Context(), nbrew.DB, query, func(row *sq.Row) []byte {
			return row.Bytes("password_hash")
		})
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			logger.Error(err.Error())
			http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
			return
		}
		err = bcrypt.CompareHashAndPassword(passwordHash, []byte(response.Password))
		if err != nil {
			response.Error = "incorrect login credentials"
			writeResponse(w, r, response)
			return
		}
		var authenticationToken [8 + 16]byte
		binary.BigEndian.PutUint64(authenticationToken[:8], uint64(time.Now().Unix()))
		_, err = rand.Read(authenticationToken[8:])
		if err != nil {
			logger.Error(err.Error())
			http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
			return
		}
		var authenticationTokenHash [8 + blake2b.Size256]byte
		checksum := sha256.Sum256([]byte(authenticationToken[8:]))
		copy(authenticationTokenHash[:8], authenticationToken[:8])
		copy(authenticationTokenHash[8:], checksum[:])
		_, err = sq.ExecContext(r.Context(), nbrew.DB, sq.CustomQuery{
			Dialect: nbrew.Dialect,
			Format:  "INSERT INTO authentications (authentication_token_hash, user_id) VALUES ({authenticationTokenHash}, {userID})",
			Values: []any{
				sq.BytesParam("authenticationTokenHash", authenticationTokenHash[:]),
				sq.Param("userID", sq.CustomQuery{
					Format: "SELECT user_id FROM users WHERE",
				}),
			},
		})
		response.AuthenticationToken = strings.TrimLeft(hex.EncodeToString(authenticationToken[:]), "0")
		writeResponse(w, r, response)
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
