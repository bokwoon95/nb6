package nb6

import (
	"net/http"
	"net/url"
)

// https://notebrew.blog/admin/@this-is-mee/createfile/

func (nbrew *Notebrew) resetpassword(w http.ResponseWriter, r *http.Request) {
	type Request struct {
		ResetToken      string `json:"reset_token,omitempty"`
		Password        string `json:"password,omitempty"`
		ConfirmPassword string `json:"confirm_password,omitempty"`
	}
	type Response struct {
		Username                  string     `json:"username,omitempty"`
		Password                  string     `json:"password,omitempty"`
		Referer                   string     `json:"referer,omitempty"`
		Errors                    url.Values `json:"errors,omitempty"`
		AuthenticationToken       string     `json:"authentication_token,omitempty"`
		IncorrectLoginCredentials bool       `json:"incorrect_login_credentials,omitempty"`
		PasswordReset             bool       `json:"password_reset,omitempty"`
	}
}
