package nb6

import (
	"net/http"
	"strings"
)

func (nbrew *Notebrew) content(w http.ResponseWriter, r *http.Request, sitePrefix, resourcePath string) {
	prefix, _, _ := strings.Cut(resourcePath, "/")
	switch prefix {
	case "admin":
		notFound(w, r)
		return
	}
	w.Write([]byte(sitePrefix + ": " + resourcePath))
}
