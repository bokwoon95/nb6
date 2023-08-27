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

	"github.com/bokwoon95/sq"
	"golang.org/x/exp/slog"
)

func (nbrew *Notebrew) filesystem(w http.ResponseWriter, r *http.Request, username string) {
	type Entry struct {
		Name    string    `json:"name,omitempty"`
		IsDir   bool      `json:"is_dir,omitempty"`
		Size    int64     `json:"size,omitempty"`
		ModTime time.Time `json:"mod_time,omitempty"`
	}
	type Response struct {
		Path    string    `json:"path"`
		IsDir   bool      `json:"is_dir"`
		Content string    `json:"content,omitempty"`
		ModTime time.Time `json:"mod_time,omitempty"`
		Entries []Entry   `json:"entries,omitempty"`
	}

	logger, ok := r.Context().Value(loggerKey).(*slog.Logger)
	if !ok {
		logger = slog.Default()
	}

	// GET /admin/themes/path/to/file.md
	// GET /admin/bokwoon.com/themes/path/to/file.md
	var sitePrefix, filePath string
	segments := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(segments) > 1 && (strings.HasPrefix(segments[1], "@") || strings.Contains(segments[1], ".")) {
		sitePrefix = segments[1]
		filePath = path.Join(segments[2:]...)
	} else {
		filePath = path.Join(segments[1:]...)
	}

	funcMap := map[string]any{
		"join":             path.Join,
		"ext":              path.Ext,
		"hasPrefix":        strings.HasPrefix,
		"hasSuffix":        strings.HasSuffix,
		"fileSizeToString": fileSizeToString,
		"base": func(s string) string {
			if s == "" {
				return "admin"
			}
			return path.Base(s)
		},
		"isEven": func(i int) bool { return i%2 == 0 },
		"siteURL": func() string {
			if strings.Contains(sitePrefix, ".") {
				return "https://" + sitePrefix + "/"
			}
			if sitePrefix != "" {
				if nbrew.MultisiteMode == "subdomain" {
					return nbrew.Protocol + strings.TrimPrefix(sitePrefix, "@") + "." + nbrew.ContentDomain + "/"
				}
				if nbrew.MultisiteMode == "subdirectory" {
					return nbrew.Protocol + nbrew.ContentDomain + "/" + sitePrefix + "/"
				}
			}
			return nbrew.Protocol + nbrew.ContentDomain + "/"
		},
		"username": func() string {
			if username == "" {
				return "user"
			}
			return "@" + username
		},
		"generateBreadcrumbLinks": func(filePath string, isDir bool) template.HTML {
			var b strings.Builder
			b.WriteString(`<a href="/admin/" class="linktext ma1">admin</a>`)
			segments := strings.Split(strings.Trim(filePath, "/"), "/")
			for i := 0; i < len(segments); i++ {
				if segments[i] == "" {
					continue
				}
				href := `/admin/` + path.Join(segments[:i+1]...) + `/`
				if i == len(segments)-1 && !isDir {
					href = strings.TrimSuffix(href, `/`)
				}
				b.WriteString(`/<a href="` + href + `" class="linktext ma1">` + segments[i] + `</a>`)
			}
			if isDir {
				b.WriteString(`/`)
			}
			return template.HTML(b.String())
		},
	}

	if r.Method != "GET" {
		http.Error(w, "404 Not Found", http.StatusNotFound)
		return
	}

	response := Response{
		Path: filePath,
	}

	authorizedSitePrefixes := make(map[string]struct{})
	if response.Path == "" && nbrew.DB != nil {
		cursor, err := sq.FetchCursorContext(r.Context(), nbrew.DB, sq.CustomQuery{
			Dialect: nbrew.Dialect,
			Format: "SELECT {*}" +
				" FROM users" +
				" JOIN site_user ON site_user.user_id = users.user_id" +
				" JOIN site ON site.site_id = site_user.site_id" +
				" WHERE users.username = {username}",
			Values: []any{
				sq.StringParam("username", username),
			},
		}, func(row *sq.Row) string {
			return row.String("site.site_name")
		})
		if err != nil {
			logger.Error(err.Error())
			http.Error(w, messageInternalServerError, http.StatusInternalServerError)
			return
		}
		defer cursor.Close()
		for cursor.Next() {
			siteName, err := cursor.Result()
			if err != nil {
				logger.Error(err.Error())
				http.Error(w, messageInternalServerError, http.StatusInternalServerError)
				return
			}
			var sitePrefix string
			if strings.Contains(siteName, ".") {
				sitePrefix = siteName
			} else {
				sitePrefix = "@" + siteName
			}
			authorizedSitePrefixes[sitePrefix] = struct{}{}
		}
		err = cursor.Close()
		if err != nil {
			logger.Error(err.Error())
			http.Error(w, messageInternalServerError, http.StatusInternalServerError)
			return
		}
	}

	if strings.HasPrefix(response.Path, "site/") && !strings.HasPrefix(response.Path, "site/themes") {
		http.Error(w, "404 Not Found", http.StatusNotFound)
		return
	}

	fileInfo, err := fs.Stat(nbrew.FS, path.Join(sitePrefix, response.Path))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			http.Error(w, "404 Not Found", http.StatusNotFound)
			return
		}
		logger.Error(err.Error())
		http.Error(w, messageInternalServerError, http.StatusInternalServerError)
		return
	}
	if fileInfo.IsDir() {
		response.IsDir = true
		dirEntries, err := nbrew.FS.ReadDir(path.Join(sitePrefix, response.Path))
		if err != nil {
			logger.Error(err.Error())
			http.Error(w, messageInternalServerError, http.StatusInternalServerError)
			return
		}
		var dirs []Entry
		var files []Entry
		for i, dirEntry := range dirEntries {
			entry := Entry{
				Name:  dirEntry.Name(),
				IsDir: dirEntry.IsDir(),
			}
			if response.Path == "" {
				if sitePrefix == "" && entry.IsDir && (strings.HasPrefix(entry.Name, "@") || strings.Contains(entry.Name, ".")) && nbrew.DB != nil {
					_, ok := authorizedSitePrefixes[entry.Name]
					if !ok {
						continue
					}
				}
				if entry.Name == "site" {
					fileInfo, err := fs.Stat(nbrew.FS, path.Join(sitePrefix, response.Path, "site/themes"))
					if err != nil && !errors.Is(err, fs.ErrNotExist) {
						logger.Error(err.Error())
						http.Error(w, messageInternalServerError, http.StatusInternalServerError)
						return
					}
					if fileInfo != nil && fileInfo.IsDir() {
						entry.Name = "site/themes"
						dirs = append(dirs, entry)
					}
					continue
				}
				if entry.Name != "notes" && entry.Name != "pages" && entry.Name != "posts" {
					continue
				}
			} else if response.Path == "site" {
				if entry.Name != "themes" {
					continue
				}
			}
			if entry.IsDir {
				dirs = append(dirs, entry)
				continue
			}
			if i >= 1000 {
				entry.Size = -1
				continue
			}
			fileInfo, err := dirEntry.Info()
			if err != nil {
				logger.Error(err.Error(), slog.String("name", entry.Name))
				http.Error(w, messageInternalServerError, http.StatusInternalServerError)
				return
			}
			entry.ModTime = fileInfo.ModTime()
			entry.Size = fileInfo.Size()
			files = append(files, entry)
		}
		response.Entries = make([]Entry, 0, len(dirs)+len(files))
		response.Entries = append(response.Entries, dirs...)
		response.Entries = append(response.Entries, files...)
		tmpl, err := template.New("file.html").Funcs(funcMap).ParseFS(rootFS, "html/file.html")
		if err != nil {
			logger.Error(err.Error())
			http.Error(w, messageInternalServerError, http.StatusInternalServerError)
			return
		}
		buf := bufPool.Get().(*bytes.Buffer)
		buf.Reset()
		defer bufPool.Put(buf)
		err = tmpl.Execute(buf, &response)
		if err != nil {
			logger.Error(err.Error())
			http.Error(w, messageInternalServerError, http.StatusInternalServerError)
			return
		}
		buf.WriteTo(w)
		return
	}
	response.Content, err = readFile(nbrew.FS, path.Join(sitePrefix, response.Path))
	if err != nil {
		logger.Error(err.Error())
		http.Error(w, messageInternalServerError, http.StatusInternalServerError)
		return
	}
	text, err := readFile(rootFS, "html/file.html")
	if err != nil {
		logger.Error(err.Error())
		http.Error(w, messageInternalServerError, http.StatusInternalServerError)
		return
	}
	tmpl, err := template.New("").Funcs(funcMap).Parse(text)
	if err != nil {
		logger.Error(err.Error())
		http.Error(w, messageInternalServerError, http.StatusInternalServerError)
		return
	}
	buf := bufPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer bufPool.Put(buf)
	err = tmpl.Execute(buf, &response)
	if err != nil {
		logger.Error(err.Error())
		http.Error(w, messageInternalServerError, http.StatusInternalServerError)
		return
	}
	buf.WriteTo(w)
}
