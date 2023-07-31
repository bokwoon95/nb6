package nb6

import "net/http"

func (nbrew *Notebrew) file(w http.ResponseWriter, r *http.Request) {
	w.Write([]byte("file: " + r.URL.RequestURI()))
}
