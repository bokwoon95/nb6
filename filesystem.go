package nb6

import (
	"bytes"
	"encoding/json"
	"errors"
	"html/template"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/bokwoon95/sq"
	"golang.org/x/exp/slog"
)

func (nbrew *Notebrew) filesystem(w http.ResponseWriter, r *http.Request, username, sitePrefix, filePath string) {
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
	if authorizedSitePrefixes[sitePrefix] {
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

	var b strings.Builder
	b.WriteString(`<a href="/admin/" class="linktext ma1">admin</a>`)
	segments := strings.Split(filePath, "/")
	if sitePrefix != "" {
		segments = append([]string{sitePrefix}, segments...)
	}
	for i := 0; i < len(segments); i++ {
		if segments[i] == "" {
			continue
		}
		href := `/admin/` + path.Join(segments[:i+1]...) + `/`
		if i == len(segments)-1 && !response.IsDir {
			href = strings.TrimSuffix(href, `/`)
		}
		b.WriteString(`/<a href="` + href + `" class="linktext ma1">` + segments[i] + `</a>`)
	}
	if response.IsDir {
		b.WriteString(`/`)
	}
	breadcrumbLinks := b.String()

	funcMap := map[string]any{
		"join":             path.Join,
		"ext":              path.Ext,
		"base":             path.Base,
		"fileSizeToString": fileSizeToString,
		"safeHTML":         func(s string) template.HTML { return template.HTML(s) },
		"isEven":           func(i int) bool { return i%2 == 0 },
		"isAdmin":          func() bool { return authorizedSitePrefixes[""] },
		"username":         func() string { return username },
		"referer":          func() string { return r.Referer() },
		"sitePrefix":       func() string { return sitePrefix },
		"breadcrumbLinks":  func() template.HTML { return template.HTML(breadcrumbLinks) },
		"isSitePrefix": func(s string) bool {
			return strings.HasPrefix(s, "@") || strings.Contains(s, ".")
		},
		"head": func(s string) string {
			head, _, _ := strings.Cut(s, "/")
			return head
		},
		"tail": func(s string) string {
			_, tail, _ := strings.Cut(s, "/")
			return tail
		},
		"neatenURL": func(s string) string {
			if strings.HasPrefix(s, "https://") {
				return strings.TrimSuffix(strings.TrimPrefix(s, "https://"), "/")
			}
			return strings.TrimSuffix(strings.TrimPrefix(s, "http://"), "/")
		},
	}

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
	var folders []Entry     // folders
	var siteFolders []Entry // we want to display site folders after normal folders, so we aggregate them separately
	var files []Entry       // files
	for _, dirEntry := range dirEntries {
		entry := Entry{
			Name:  dirEntry.Name(),
			IsDir: dirEntry.IsDir(),
		}
		// For the root path, show only folders if they are "notes", "pages",
		// "posts" or "site" or if they are site folders.
		if response.Path == "" {
			// Skip files.
			if !entry.IsDir {
				continue
			}
			switch entry.Name {
			case "notes", "pages", "posts":
				folders = append(folders, entry)
			case "site":
				folders = append(folders, entry)
				fileInfo, err := fs.Stat(nbrew.FS, path.Join(sitePrefix, "site/themes"))
				if err != nil && !errors.Is(err, fs.ErrNotExist) {
					logger.Error(err.Error())
					http.Error(w, messageInternalServerError, http.StatusInternalServerError)
					return
				}
				if fileInfo != nil && fileInfo.IsDir() {
					folders = append(folders, Entry{
						Name:  "site/themes",
						IsDir: true,
					})
				}
			default:
				// If it is a site folder.
				if sitePrefix == "" && (strings.HasPrefix(entry.Name, "@") || strings.Contains(entry.Name, ".")) {
					// If the current user is authorized to see it.
					if nbrew.DB == nil || authorizedSitePrefixes[entry.Name] {
						siteFolders = append(siteFolders, entry)
					}
				}
			}
			continue
		}
		if entry.IsDir {
			folders = append(folders, entry)
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
	response.Entries = make([]Entry, 0, len(folders)+len(siteFolders)+len(files))
	response.Entries = append(response.Entries, folders...)
	response.Entries = append(response.Entries, siteFolders...)
	response.Entries = append(response.Entries, files...)
	accept, _, _ := mime.ParseMediaType(r.Header.Get("Accept"))
	if accept == "application/json" {
		response.Alerts = nil
		b, err := json.Marshal(&response)
		if err != nil {
			logger.Error(err.Error())
			http.Error(w, messageInternalServerError, http.StatusInternalServerError)
			return
		}
		w.Write(b)
		return
	}
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
	w.Header().Add("Content-Security-Policy", defaultContentSecurityPolicy)
	buf.WriteTo(w)
}