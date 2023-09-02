package nb6

import (
	"net/http"

	"golang.org/x/exp/slog"
)

func (nbrew *Notebrew) deleteSite(w http.ResponseWriter, r *http.Request, username, sitePrefix string) {
	type Request struct {
		Name string `json:"name,omitempty"`
	}
	type Response struct {
		Errors []string `json:"errors,omitempty"`
	}

	logger, ok := r.Context().Value(loggerKey).(*slog.Logger)
	if !ok {
		logger = slog.Default()
	}
	_ = logger

	switch r.Method {
	case "GET":
	case "POST":
	default:
		http.Error(w, "405 Method Not Allowed", http.StatusMethodNotAllowed)
	}
}
