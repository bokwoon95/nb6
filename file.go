package nb6

import (
	"bytes"
	"errors"
	"html/template"
	"io/fs"
	"net/http"
	"net/url"
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
		Symlink string    `json:"symlink,omitempty"`
	}
	type Response struct {
		Path           string     `json:"path"`
		IsDir          bool       `json:"is_dir"`
		Content        string     `json:"content,omitempty"`
		ModTime        time.Time  `json:"mod_time,omitempty"`
		Entries        []Entry    `json:"entries,omitempty"`
		Alerts         url.Values `json:"alerts,omitempty"`
		ContentSiteURL string     `json:"content_site_url,omitempty"`
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

	if r.Method != "GET" {
		http.Error(w, "405 Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	var response Response
	_, err := nbrew.getSession(r, "flash", &response)
	if err != nil {
		logger.Error(err.Error())
	}
	nbrew.clearSession(w, r, "flash")
	response.Path = filePath

	authorizedSitePrefixes := make(map[string]bool)
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
			} else if siteName != "" {
				sitePrefix = "@" + siteName
			}
			authorizedSitePrefixes[sitePrefix] = true
		}
		err = cursor.Close()
		if err != nil {
			logger.Error(err.Error())
			http.Error(w, messageInternalServerError, http.StatusInternalServerError)
			return
		}
	}
	if authorizedSitePrefixes[sitePrefix] || authorizedSitePrefixes[""] {
		if strings.Contains(sitePrefix, ".") {
			response.ContentSiteURL = "https://" + sitePrefix + "/"
		} else if sitePrefix != "" {
			if nbrew.MultisiteMode == "subdomain" {
				response.ContentSiteURL = nbrew.Scheme + strings.TrimPrefix(sitePrefix, "@") + "." + nbrew.ContentDomain + "/"
			} else if nbrew.MultisiteMode == "subdirectory" {
				response.ContentSiteURL = nbrew.Scheme + nbrew.ContentDomain + "/" + sitePrefix + "/"
			}
		}
		if response.ContentSiteURL == "" {
			response.ContentSiteURL = nbrew.Scheme + nbrew.ContentDomain + "/"
		}
	}

	funcMap := map[string]any{
		"join":             path.Join,
		"ext":              path.Ext,
		"base":             path.Base,
		"fileSizeToString": fileSizeToString,
		"head": func(s string) string {
			head, _, _ := strings.Cut(s, "/")
			return head
		},
		"neatenURL": func(s string) string {
			if strings.HasPrefix(s, "https://") {
				return strings.TrimSuffix(strings.TrimPrefix(s, "https://"), "/")
			}
			return strings.TrimSuffix(strings.TrimPrefix(s, "http://"), "/")
		},
		"hasThemesFolder": func() bool {
			fileInfo, err := fs.Stat(nbrew.FS, path.Join(sitePrefix, "site/themes"))
			if err != nil && !errors.Is(err, fs.ErrNotExist) {
				logger.Error(err.Error())
				return false
			}
			return fileInfo != nil && fileInfo.IsDir()
		},
		"isSitePrefix": func(s string) bool {
			return strings.HasPrefix(s, "@") || strings.Contains(s, ".")
		},
		"safeHTML": func(s string) template.HTML { return template.HTML(s) },
		"isEven":   func(i int) bool { return i%2 == 0 },
		"isAdmin":  func() bool { return authorizedSitePrefixes[""] },
		"username": func() string { return username },
		"generateBreadcrumbLinks": func(filePath string, isDir bool) template.HTML {
			var b strings.Builder
			b.WriteString(`<a href="/admin/" class="linktext ma1">admin</a>`)
			segments := strings.Split(strings.Trim(filePath, "/"), "/")
			if sitePrefix != "" {
				segments = append([]string{sitePrefix}, segments...)
			}
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
	response.IsDir = fileInfo.IsDir()
	if !response.IsDir {
		response.Content, err = readFile(nbrew.FS, path.Join(sitePrefix, response.Path))
		if err != nil {
			logger.Error(err.Error())
			http.Error(w, messageInternalServerError, http.StatusInternalServerError)
			return
		}
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
		w.Header().Add("Content-Security-Policy", defaultContentSecurityPolicy)
		buf.WriteTo(w)
		return
	}
	dirEntries, err := nbrew.FS.ReadDir(path.Join(sitePrefix, response.Path))
	if err != nil {
		logger.Error(err.Error())
		http.Error(w, messageInternalServerError, http.StatusInternalServerError)
		return
	}
	var dirs []Entry
	var files []Entry
	for _, dirEntry := range dirEntries {
		entry := Entry{
			Name:  dirEntry.Name(),
			IsDir: dirEntry.IsDir(),
		}
		// For these specific paths, only specific folders are shown (otherwise
		// the entry will be skipped).
		if response.Path == "" {
			// Don't show files.
			if !entry.IsDir {
				continue
			}
			if sitePrefix == "" && (strings.HasPrefix(entry.Name, "@") || strings.Contains(entry.Name, ".")) {
				// Don't show site folders that the current user is not
				// authorized to see.
				if nbrew.DB != nil && !authorizedSitePrefixes[""] && !authorizedSitePrefixes[entry.Name] {
					continue
				}
			} else {
				// Don't show folder if it isn't "notes", "pages", "posts" or "site".
				if entry.Name != "notes" && entry.Name != "pages" && entry.Name != "posts" && entry.Name != "site" {
					continue
				}
			}
		}
		if entry.IsDir {
			dirs = append(dirs, entry)
			continue
		}
		// Only call dirEntry.Info() for the first 1000 files (it is
		// expensive).
		if len(files) <= 1000 {
			fileInfo, err := dirEntry.Info()
			if err != nil {
				logger.Error(err.Error(), slog.String("name", entry.Name))
				http.Error(w, messageInternalServerError, http.StatusInternalServerError)
				return
			}
			entry.ModTime = fileInfo.ModTime()
			entry.Size = fileInfo.Size()
		}
		files = append(files, entry)
	}
	response.Entries = make([]Entry, 0, len(dirs)+len(files))
	response.Entries = append(response.Entries, dirs...)
	response.Entries = append(response.Entries, files...)
	tmpl, err := template.New("dir.html").Funcs(funcMap).ParseFS(rootFS, "html/dir.html")
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
