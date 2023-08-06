package nb6

import (
	"bytes"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/bokwoon95/sq"
	"golang.org/x/exp/slog"
)

func (nbrew *Notebrew) dir(w http.ResponseWriter, r *http.Request, username string) {
	type Entry struct {
		Name    string    `json:"name,omitempty"`
		IsDir   bool      `json:"is_dir,omitempty"`
		Size    int64     `json:"size,omitempty"`
		ModTime time.Time `json:"mod_time,omitempty"`
	}
	type Response struct {
		Path    string  `json:"path"`
		Entries []Entry `json:"entries"`
	}

	logger, ok := r.Context().Value(loggerKey).(*slog.Logger)
	if !ok {
		logger = slog.Default()
	}

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
		"generateBreadcrumbLinks": func(filePath string) template.HTML {
			var b strings.Builder
			b.WriteString(`<a href="/admin/" class="linktext ma1">admin</a>/`)
			segments := strings.Split(strings.Trim(filePath, "/"), "/")
			for i := 0; i < len(segments); i++ {
				if segments[i] == "" {
					continue
				}
				b.WriteString(fmt.Sprintf(`<a href="/admin/%s/" class="linktext ma1">%s</a>/`, path.Join(segments[:i+1]...), segments[i]))
			}
			return template.HTML(b.String())
		},
	}

	if r.Method != "GET" {
		http.Error(w, "404 Not Found", http.StatusNotFound)
		return
	}

	authorizedSitePrefixes := make(map[string]struct{})
	if nbrew.DB != nil {
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
			http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
			return
		}
		defer cursor.Close()
		for cursor.Next() {
			siteName, err := cursor.Result()
			if err != nil {
				logger.Error(err.Error())
				http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
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
			http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
			return
		}
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
	var dirs []Entry
	var files []Entry
	for _, dirEntry := range dirEntries {
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
			if entry.Name != "notes" && entry.Name != "pages" && entry.Name != "posts" && entry.Name != "themes" {
				continue
			}
		}
		if entry.IsDir {
			dirs = append(dirs, entry)
		} else {
			fileInfo, err := dirEntry.Info()
			if err != nil {
				logger.Error(err.Error(), slog.String("name", entry.Name))
				http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
				return
			}
			entry.ModTime = fileInfo.ModTime()
			entry.Size = fileInfo.Size()
			files = append(files, entry)
		}
	}
	response.Entries = make([]Entry, 0, len(dirs)+len(files))
	response.Entries = append(response.Entries, dirs...)
	response.Entries = append(response.Entries, files...)
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
