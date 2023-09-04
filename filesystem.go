package nb6

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"html/template"
	"io"
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
		Title   string    `json:"title,omitempty"`
		Preview string    `json:"preview,omitempty"`
		Size    int64     `json:"size,omitempty"`
		ModTime time.Time `json:"mod_time,omitempty"`
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

	fileInfo, err := fs.Stat(nbrew.FS, path.Join(sitePrefix, response.Path))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			notFound(w, r)
			return
		}
		logger.Error(err.Error())
		internalServerError(w, r)
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
		"trimPrefix":       strings.TrimPrefix,
		"fileSizeToString": fileSizeToString,
		"safeHTML":         func(s string) template.HTML { return template.HTML(s) },
		"isEven":           func(i int) bool { return i%2 == 0 },
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
			internalServerError(w, r)
			return
		}
		tmpl, err := template.New("filesystem_file.html").Funcs(funcMap).ParseFS(rootFS, "html/filesystem_file.html")
		if err != nil {
			logger.Error(err.Error())
			internalServerError(w, r)
			return
		}
		buf := bufPool.Get().(*bytes.Buffer)
		buf.Reset()
		defer bufPool.Put(buf)
		err = tmpl.Execute(buf, &response)
		if err != nil {
			logger.Error(err.Error())
			internalServerError(w, r)
			return
		}
		w.Header().Add("Content-Security-Policy", defaultContentSecurityPolicy)
		buf.WriteTo(w)
		return
	}

	// authorizedSitePrefixes tracks which sitePrefixes the current user is
	// authorized to see. It is populated only when required for performance,
	// which is when the user is on a page where they are able to see their
	// list of sites.
	//
	// If authorizedSitePrefixes is nil, it is not valid and should not be used
	// to check for sitePrefix authorization.
	var authorizedSitePrefixes map[string]bool
	if sitePrefix == "" && response.Path == "" && nbrew.DB != nil {
		authorizedSitePrefixes = make(map[string]bool)
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
			internalServerError(w, r)
			return
		}
		defer cursor.Close()
		for cursor.Next() {
			siteName, err := cursor.Result()
			if err != nil {
				logger.Error(err.Error())
				internalServerError(w, r)
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
			internalServerError(w, r)
			return
		}
	}

	var folders []Entry     // folders
	var siteFolders []Entry // we want to display site folders after normal folders, so we aggregate them separately
	var files []Entry       // files
	dirEntries, err := nbrew.FS.ReadDir(path.Join(sitePrefix, response.Path))
	if err != nil {
		logger.Error(err.Error())
		internalServerError(w, r)
		return
	}
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
			case "notes", "pages", "posts", "site":
				// If current user is not authorized to the empty site, don't
				// show any of its folders.
				if authorizedSitePrefixes != nil && !authorizedSitePrefixes[""] {
					continue
				}
				folders = append(folders, entry)
				// Special case for site: insert an additional entry for
				// site/themes if it exists.
				if entry.Name == "site" {
					fileInfo, err := fs.Stat(nbrew.FS, path.Join(sitePrefix, "site/themes"))
					if err != nil && !errors.Is(err, fs.ErrNotExist) {
						logger.Error(err.Error())
						internalServerError(w, r)
						return
					}
					if fileInfo != nil && fileInfo.IsDir() {
						folders = append(folders, Entry{
							Name:  "site/themes",
							IsDir: true,
						})
					}
				}
			default:
				// If it is a site folder.
				if strings.HasPrefix(entry.Name, "@") || strings.Contains(entry.Name, ".") {
					// If the current user is authorized to see it.
					if authorizedSitePrefixes != nil && authorizedSitePrefixes[entry.Name] {
						siteFolders = append(siteFolders, entry)
					}
				}
			}
			continue
		}
		// TODO: Don't nest more than one level for notes and posts.
		// TODO: Don't show anything other than .md and .txt files for notes and posts.
		if entry.IsDir {
			folders = append(folders, entry)
			continue
		}
		// Don't call dirEntry.Info() for more than 10_000 files.
		if len(files) > 10_000 {
			files = append(files, entry)
			continue
		}
		fileInfo, err := dirEntry.Info()
		if err != nil {
			logger.Error(err.Error(), slog.String("name", entry.Name))
			internalServerError(w, r)
			return
		}
		entry.ModTime = fileInfo.ModTime()
		entry.Size = fileInfo.Size()
		head, _, _ := strings.Cut(response.Path, "/")
		if head == "notes" || head == "posts" {
			file, err := nbrew.FS.Open(path.Join(sitePrefix, response.Path, entry.Name))
			if err != nil {
				logger.Error(err.Error(), slog.String("name", entry.Name))
				internalServerError(w, r)
				return
			}
			var title, preview []byte
			reader := bufio.NewReader(file)
			done := false
			for {
				if done {
					break
				}
				text, err := reader.ReadBytes('\n')
				if err != nil {
					if err == io.EOF {
						done = true
					} else {
						logger.Error(err.Error(), slog.String("name", entry.Name))
						internalServerError(w, r)
						return
					}
				}
				text = bytes.TrimSpace(text)
				if len(text) == 0 {
					continue
				}
				if len(title) == 0 {
					title = text
					continue
				}
				if len(preview) == 0 {
					preview = text
					continue
				}
				break
			}
			if e := file.Close(); e != nil {
				logger.Error(e.Error())
			}
			if len(title) > 0 {
				var b strings.Builder
				stripMarkdownStyles(&b, title)
				entry.Title = b.String()
			}
			if len(preview) > 0 {
				var b strings.Builder
				stripMarkdownStyles(&b, preview)
				entry.Preview = b.String()
			}
		}
		files = append(files, entry)
	}
	response.Entries = make([]Entry, 0, len(folders)+len(siteFolders)+len(files))
	response.Entries = append(response.Entries, folders...)
	response.Entries = append(response.Entries, siteFolders...)
	response.Entries = append(response.Entries, files...)
	accept, _, _ := mime.ParseMediaType(r.Header.Get("Accept"))
	if accept == "application/json" {
		w.Header().Set("Content-Type", "application/json")
		response.Alerts = nil
		b, err := json.Marshal(&response)
		if err != nil {
			logger.Error(err.Error())
			internalServerError(w, r)
			return
		}
		w.Write(b)
		return
	}
	tmpl, err := template.New("filesystem_folder.html").Funcs(funcMap).ParseFS(rootFS, "html/filesystem_folder.html")
	if err != nil {
		logger.Error(err.Error())
		internalServerError(w, r)
		return
	}
	buf := bufPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer bufPool.Put(buf)
	err = tmpl.Execute(buf, &response)
	if err != nil {
		logger.Error(err.Error())
		internalServerError(w, r)
		return
	}
	w.Header().Add("Content-Security-Policy", defaultContentSecurityPolicy)
	buf.WriteTo(w)
}
