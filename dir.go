package nb6

import (
	"bytes"
	"errors"
	"html/template"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"

	"golang.org/x/exp/slog"
)

func (nbrew *Notebrew) dir(w http.ResponseWriter, r *http.Request) {
	type Entry struct {
		Name    string     `json:"name,omitempty"`
		IsDir   bool       `json:"is_dir,omitempty"`
		Size    *int64     `json:"size,omitempty"`
		ModTime *time.Time `json:"mod_time,omitempty"`
	}
	type Response struct {
		Path    string  `json:"path"`
		Entries []Entry `json:"entries"`
	}

	logger, ok := r.Context().Value(loggerKey).(*slog.Logger)
	if !ok {
		logger = slog.Default()
	}

	var funcMap = map[string]any{
		"base": path.Base,
		"join": path.Join,
		"generateBreadcrumbLinks": func(filePath string) template.HTML {
			return template.HTML(filePath)
		},
	}

	var sitePrefix, filePath string
	segments := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(segments) > 1 && (strings.HasPrefix(segments[1], "@") || strings.Contains(segments[1], ".")) {
		sitePrefix = segments[1]
		filePath = path.Join(segments[2:]...)
	} else {
		filePath = path.Join(segments[1:]...)
	}

	if r.Method != "GET" {
		http.Error(w, "404 Not Found", http.StatusNotFound)
		return
	}

	response := Response{
		Path: filePath,
	}
	dirEntries, err := nbrew.FS.ReadDir(path.Join(sitePrefix, response.Path))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			http.Error(w, "404 Not Found", http.StatusNotFound)
			return
		}
		logger.Error(err.Error())
		http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
		return
	}
	for _, dirEntry := range dirEntries {
		response.Entries = append(response.Entries, Entry{
			Name:  dirEntry.Name(),
			IsDir: dirEntry.IsDir(),
		})
	}
	text, err := readFile(rootFS, "html/dir.html")
	if err != nil {
		logger.Error(err.Error())
		http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
		return
	}
	tmpl, err := template.New("").Funcs(funcMap).Parse(text)
	if err != nil {
		logger.Error(err.Error())
		http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
		return
	}
	buf := bufPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer bufPool.Put(buf)
	err = tmpl.Execute(buf, &response)
	if err != nil {
		logger.Error(err.Error())
		http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
		return
	}
	buf.WriteTo(w)
}
