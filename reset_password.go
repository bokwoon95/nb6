package nb6

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"mime"
	"net/http"
	"net/url"
	"unicode/utf8"

	"github.com/bokwoon95/sq"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/crypto/blake2b"
	"golang.org/x/exp/slog"
)

// https://notebrew.blog/admin/@this-is-mee/createfile/

func (nbrew *Notebrew) resetPassword(w http.ResponseWriter, r *http.Request) {
	type Request struct {
		Token           string `json:"token,omitempty"`
		Password        string `json:"password,omitempty"`
		ConfirmPassword string `json:"confirm_password,omitempty"`
	}
	type Response struct {
		Token  string     `json:"token,omitempty"`
		Errors url.Values `json:"errors,omitempty"`
	}

	logger, ok := r.Context().Value(loggerKey).(*slog.Logger)
	if !ok {
		logger = slog.Default()
	}

	if nbrew.DB == nil {
		notFound(w, r)
		return
	}

	switch r.Method {
	case "GET":
		err := r.ParseForm()
		if err != nil {
			http.Error(w, fmt.Sprintf("invalid request query or body: %v", err), http.StatusBadRequest)
			return
		}
		token := r.Form.Get("token")
		if token == "" {
			http.Error(w, "token invalid", http.StatusBadRequest)
			return
		}
		resetToken, err := hex.DecodeString(fmt.Sprintf("%048s", token))
		if err != nil {
			http.Error(w, "token invalid", http.StatusBadRequest)
			return
		}
		checksum := blake2b.Sum256([]byte(resetToken[8:]))
		var resetTokenHash [8 + blake2b.Size256]byte
		copy(resetTokenHash[:8], resetToken[:8])
		copy(resetTokenHash[8:], checksum[:])
		exists, err := sq.FetchExistsContext(r.Context(), nbrew.DB, sq.CustomQuery{
			Dialect: nbrew.Dialect,
			Format:  "SELECT 1 FROM users WHERE reset_token_hash = {resetTokenHash}",
			Values: []any{
				sq.BytesParam("resetTokenHash", resetTokenHash[:]),
			},
		})
		if err != nil {
			logger.Error(err.Error())
			internalServerError(w, r, err)
			return
		}
		if !exists {
			http.Error(w, "token invalid", http.StatusBadRequest)
			return
		}

		var response Response
		ok, err := nbrew.getSession(r, "flash", &response)
		if err != nil {
			logger.Error(err.Error())
		} else if !ok {
			response.Token = token
		}
		nbrew.clearSession(w, r, "flash")

		buf := bufPool.Get().(*bytes.Buffer)
		buf.Reset()
		defer bufPool.Put(buf)
		tmpl, err := template.ParseFS(rootFS, "html/reset_password.html")
		if err != nil {
			logger.Error(err.Error())
			internalServerError(w, r, err)
			return
		}
		err = tmpl.Execute(buf, &response)
		if err != nil {
			logger.Error(err.Error())
			internalServerError(w, r, err)
			return
		}
		w.Header().Add("Content-Security-Policy", defaultContentSecurityPolicy)
		buf.WriteTo(w)
	case "POST":
		writeResponse := func(w http.ResponseWriter, r *http.Request, response Response) {
			accept, _, _ := mime.ParseMediaType(r.Header.Get("Accept"))
			if accept == "application/json" {
				w.Header().Set("Content-Type", "application/json")
				b, err := json.Marshal(&response)
				if err != nil {
					logger.Error(err.Error())
					internalServerError(w, r, err)
					return
				}
				w.Write(b)
				return
			}
			if len(response.Errors) > 0 {
				err := nbrew.setSession(w, r, "flash", &response)
				if err != nil {
					logger.Error(err.Error())
					internalServerError(w, r, err)
					return
				}
				http.Redirect(w, r, r.URL.String(), http.StatusFound)
				return
			}
			err := nbrew.setSession(w, r, "flash", map[string]any{
				"password_reset": true,
			})
			if err != nil {
				logger.Error(err.Error())
				internalServerError(w, r, err)
				return
			}
			http.Redirect(w, r, nbrew.Scheme+nbrew.AdminDomain+"/admin/login/", http.StatusFound)
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
				internalServerError(w, r, err)
				return
			}
		case "application/x-www-form-urlencoded":
			err := r.ParseForm()
			if err != nil {
				http.Error(w, fmt.Sprintf("400 Bad Request: %s", err), http.StatusBadRequest)
				return
			}
			request.Token = r.Form.Get("token")
			request.Password = r.Form.Get("password")
			request.ConfirmPassword = r.Form.Get("confirm_password")
		default:
			http.Error(w, "415 Unsupported Media Type", http.StatusUnsupportedMediaType)
			return
		}

		response := Response{
			Token:  "",
			Errors: make(url.Values),
		}
		if utf8.RuneCountInString(request.Password) < 8 {
			response.Errors.Add("", "Password must be at least 8 characters")
			writeResponse(w, r, response)
			return
		}
		if request.ConfirmPassword != request.Password {
			response.Errors.Add("", "Passwords do not match")
			writeResponse(w, r, response)
			return
		}
		passwordHash, err := bcrypt.GenerateFromPassword([]byte(request.Password), bcrypt.DefaultCost)
		if err != nil {
			logger.Error(err.Error())
			internalServerError(w, r, err)
			return
		}
		if request.Token == "" {
			http.Error(w, "token invalid", http.StatusBadRequest)
			return
		}
		resetToken, err := hex.DecodeString(fmt.Sprintf("%048s", request.Token))
		if err != nil {
			http.Error(w, "token invalid", http.StatusBadRequest)
			return
		}
		checksum := blake2b.Sum256([]byte(resetToken[8:]))
		var resetTokenHash [8 + blake2b.Size256]byte
		copy(resetTokenHash[:8], resetToken[:8])
		copy(resetTokenHash[8:], checksum[:])
		tx, err := nbrew.DB.Begin()
		if err != nil {
			logger.Error(err.Error())
			internalServerError(w, r, err)
			return
		}
		defer tx.Rollback()
		_, err = sq.ExecContext(r.Context(), tx, sq.CustomQuery{
			Dialect: nbrew.Dialect,
			Format: "DELETE FROM authentication WHERE EXISTS (SELECT 1" +
				" FROM users" +
				" WHERE users.user_id = authentication.user_id" +
				" AND users.reset_token_hash = {resetTokenHash}" +
				")",
			Values: []any{
				sq.BytesParam("resetTokenHash", resetTokenHash[:]),
			},
		})
		if err != nil {
			logger.Error(err.Error())
			internalServerError(w, r, err)
			return
		}
		result, err := sq.ExecContext(r.Context(), tx, sq.CustomQuery{
			Dialect: nbrew.Dialect,
			Format: "UPDATE users" +
				" SET password_hash = {passwordHash}" +
				", reset_token_hash = NULL" +
				" WHERE reset_token_hash = {resetTokenHash}",
			Values: []any{
				sq.StringParam("passwordHash", string(passwordHash)),
				sq.BytesParam("resetTokenHash", resetTokenHash[:]),
			},
		})
		err = tx.Commit()
		if err != nil {
			logger.Error(err.Error())
			internalServerError(w, r, err)
			return
		}
		if result.RowsAffected == 0 {
			http.Error(w, "token invalid", http.StatusBadRequest)
			return
		}
		writeResponse(w, r, response)
	default:
		http.Error(w, "405 Method Not Allowed", http.StatusMethodNotAllowed)
	}
}
