package nb6

import (
	"net/http"
	"strings"
)

func (nbrew *Notebrew) content(w http.ResponseWriter, r *http.Request, sitePrefix, resourcePath string) {
	prefix, _, _ := strings.Cut(resourcePath, "/")
	switch prefix {
	case "admin":
		http.Error(w, "404 Not Found", http.StatusNotFound)
		return
	}
	w.Write([]byte(sitePrefix + ": " + resourcePath))
}
