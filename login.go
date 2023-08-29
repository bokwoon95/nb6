package nb6

import (
	"bytes"
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"mime"
	"net/http"
	"net/url"
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
	type Response struct {
		Username                  string     `json:"username,omitempty"`
		Password                  string     `json:"password,omitempty"`
		Referer                   string     `json:"referer,omitempty"`
		Errors                    url.Values `json:"errors,omitempty"`
		AuthenticationToken       string     `json:"authentication_token,omitempty"`
		IncorrectLoginCredentials bool       `json:"incorrect_login_credentials,omitempty"`
		AlreadyLoggedIn           bool       `json:"already_logged_in,omitempty"`
		PasswordReset             bool       `json:"password_reset,omitempty"`
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

	var alreadyLoggedIn bool
	authenticationTokenHash := getAuthenticationTokenHash(r)
	if authenticationTokenHash != nil {
		exists, err := sq.FetchExistsContext(r.Context(), nbrew.DB, sq.CustomQuery{
			Format: "SELECT 1 FROM authentication WHERE authentication_token_hash = {authenticationTokenHash}",
			Values: []any{
				sq.BytesParam("authenticationTokenHash", authenticationTokenHash),
			},
		})
		if err != nil {
			logger.Error(err.Error())
		} else if exists {
			alreadyLoggedIn = true
		}
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
		response.AlreadyLoggedIn = alreadyLoggedIn

		buf := bufPool.Get().(*bytes.Buffer)
		buf.Reset()
		defer bufPool.Put(buf)
		tmpl, err := template.ParseFS(rootFS, "html/login.html")
		if err != nil {
			logger.Error(err.Error())
			http.Error(w, messageInternalServerError, http.StatusInternalServerError)
			return
		}
		err = tmpl.Execute(buf, &response)
		if err != nil {
			logger.Error(err.Error())
			http.Error(w, messageInternalServerError, http.StatusInternalServerError)
			return
		}
		w.Header().Add("Content-Security-Policy", defaultContentSecurityPolicy)
		buf.WriteTo(w)
	case "POST":
		writeResponse := func(w http.ResponseWriter, r *http.Request, response Response) {
			accept, _, _ := mime.ParseMediaType(r.Header.Get("Accept"))
			if accept == "application/json" {
				b, err := json.Marshal(&response)
				if err != nil {
					logger.Error(err.Error())
					http.Error(w, messageInternalServerError, http.StatusInternalServerError)
					return
				}
				w.Write(b)
				return
			}
			if len(response.Errors) > 0 || response.IncorrectLoginCredentials || response.AlreadyLoggedIn {
				err := nbrew.setSession(w, r, "flash", &response)
				if err != nil {
					logger.Error(err.Error())
					http.Error(w, messageInternalServerError, http.StatusInternalServerError)
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
				http.Error(w, messageInternalServerError, http.StatusInternalServerError)
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

		response := Response{
			Username:        request.Username,
			Password:        request.Password,
			Referer:         request.Referer,
			Errors:          make(url.Values),
			AlreadyLoggedIn: alreadyLoggedIn,
			PasswordReset:   false,
		}
		if response.AlreadyLoggedIn {
			writeResponse(w, r, response)
			return
		}
		if response.Username == "" {
			response.Errors.Add("username", "cannot be empty")
		}
		if response.Password == "" {
			response.Errors.Add("password", "cannot be empty")
		}
		if len(response.Errors) > 0 {
			writeResponse(w, r, response)
			return
		}

		var email string
		if !strings.HasPrefix(response.Username, "@") && strings.Contains(response.Username, "@") {
			email = response.Username
		}
		var err error
		var passwordHash []byte
		if email != "" {
			passwordHash, err = sq.FetchOneContext(r.Context(), nbrew.DB, sq.CustomQuery{
				Format: "SELECT {*} FROM users WHERE email = {email}",
				Values: []any{
					sq.StringParam("email", email),
				},
			}, func(row *sq.Row) []byte {
				return row.Bytes("password_hash")
			})
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				logger.Error(err.Error())
				http.Error(w, messageInternalServerError, http.StatusInternalServerError)
				return
			}
		} else {
			username := strings.TrimPrefix(response.Username, "@")
			if username == "" {
				response.IncorrectLoginCredentials = true
				writeResponse(w, r, response)
				return
			}
			passwordHash, err = sq.FetchOneContext(r.Context(), nbrew.DB, sq.CustomQuery{
				Dialect: nbrew.Dialect,
				Format:  "SELECT {*} FROM users WHERE username = {username}",
				Values: []any{
					sq.StringParam("username", strings.TrimPrefix(response.Username, "@")),
				},
			}, func(row *sq.Row) []byte {
				return row.Bytes("password_hash")
			})
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				logger.Error(err.Error())
				http.Error(w, messageInternalServerError, http.StatusInternalServerError)
				return
			}
		}
		err = bcrypt.CompareHashAndPassword(passwordHash, []byte(response.Password))
		if err != nil {
			response.IncorrectLoginCredentials = true
			writeResponse(w, r, response)
			return
		}
		var authenticationToken [8 + 16]byte
		binary.BigEndian.PutUint64(authenticationToken[:8], uint64(time.Now().Unix()))
		_, err = rand.Read(authenticationToken[8:])
		if err != nil {
			logger.Error(err.Error())
			http.Error(w, messageInternalServerError, http.StatusInternalServerError)
			return
		}
		var authenticationTokenHash [8 + blake2b.Size256]byte
		checksum := blake2b.Sum256([]byte(authenticationToken[8:]))
		copy(authenticationTokenHash[:8], authenticationToken[:8])
		copy(authenticationTokenHash[8:], checksum[:])
		if email != "" {
			_, err = sq.ExecContext(r.Context(), nbrew.DB, sq.CustomQuery{
				Dialect: nbrew.Dialect,
				Format: "INSERT INTO authentication (authentication_token_hash, user_id)" +
					" VALUES ({authenticationTokenHash}, (SELECT user_id FROM users WHERE email = {email}))",
				Values: []any{
					sq.BytesParam("authenticationTokenHash", authenticationTokenHash[:]),
					sq.StringParam("email", email),
				},
			})
			if err != nil {
				logger.Error(err.Error())
				http.Error(w, messageInternalServerError, http.StatusInternalServerError)
				return
			}
		} else {
			_, err = sq.ExecContext(r.Context(), nbrew.DB, sq.CustomQuery{
				Dialect: nbrew.Dialect,
				Format: "INSERT INTO authentication (authentication_token_hash, user_id)" +
					" VALUES ({authenticationTokenHash}, (SELECT user_id FROM users WHERE username = {username}))",
				Values: []any{
					sq.BytesParam("authenticationTokenHash", authenticationTokenHash[:]),
					sq.StringParam("username", strings.TrimPrefix(response.Username, "@")),
				},
			})
			if err != nil {
				logger.Error(err.Error())
				http.Error(w, messageInternalServerError, http.StatusInternalServerError)
				return
			}
		}
		response.AuthenticationToken = strings.TrimLeft(hex.EncodeToString(authenticationToken[:]), "0")
		writeResponse(w, r, response)
	default:
		http.Error(w, "405 Method Not Allowed", http.StatusMethodNotAllowed)
	}
}
