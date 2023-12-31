package nb6

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/bokwoon95/sq"
	"golang.org/x/exp/slog"
)

func (nbrew *Notebrew) filesystem(w http.ResponseWriter, r *http.Request, username, sitePrefix, filePath string) {
	type Entry struct {
		Name    string     `json:"name,omitempty"`
		IsDir   bool       `json:"is_dir,omitempty"`
		Title   string     `json:"title,omitempty"`
		Preview string     `json:"preview,omitempty"`
		Size    int64      `json:"size,omitempty"`
		ModTime *time.Time `json:"mod_time,omitempty"`
	}
	type Response struct {
		Path           string     `json:"path"`
		IsDir          bool       `json:"is_dir"`
		Content        string     `json:"content,omitempty"`
		ModTime        *time.Time `json:"mod_time,omitempty"`
		Entries        []Entry    `json:"entries,omitempty"`
		Alerts         url.Values `json:"alerts,omitempty"`
		ContentSiteURL string     `json:"content_site_url,omitempty"`
		Sort           string     `json:"sort,omitempty"`
		Order          string     `json:"order,omitempty"`
	}

	logger, ok := r.Context().Value(loggerKey).(*slog.Logger)
	if !ok {
		logger = slog.Default()
	}

	if r.Method != "GET" {
		http.Error(w, "405 Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	err := r.ParseForm()
	if err != nil {
		http.Error(w, fmt.Sprintf("400 Bad Request: %s", err), http.StatusBadRequest)
		return
	}

	var response Response
	_, err = nbrew.getSession(r, "flash", &response)
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
	head, _, _ := strings.Cut(response.Path, "/")
	if response.IsDir && (head == "notes" || head == "posts") {
		n := strings.Count(response.Path, "/")
		if n > 1 {
			notFound(w, r)
			return
		}
	}
	response.Sort = strings.ToLower(strings.TrimSpace(r.Form.Get("sort")))
	if response.Sort == "" {
		cookie, _ := r.Cookie("sort")
		if cookie != nil {
			response.Sort = cookie.Value
		}
	}
	switch response.Sort {
	case "name", "created", "edited", "title":
		break
	default:
		if head == "notes" || head == "posts" {
			response.Sort = "created"
		} else {
			response.Sort = "name"
		}
	}
	response.Order = strings.ToLower(strings.TrimSpace(r.Form.Get("order")))
	if response.Order == "" {
		cookie, _ := r.Cookie("order")
		if cookie != nil {
			response.Order = cookie.Value
		}
	}
	switch response.Order {
	case "asc", "desc":
		break
	default:
		if response.Sort == "created" || response.Sort == "edited" {
			response.Order = "desc"
		} else {
			response.Order = "asc"
		}
	}

	fileInfo, err := fs.Stat(nbrew.FS, path.Join(sitePrefix, response.Path))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			notFound(w, r)
			return
		}
		logger.Error(err.Error())
		internalServerError(w, r, err)
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
		"dir":              path.Dir,
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
		"getTitle": func(s string) string {
			done := false
			for {
				if done {
					return ""
				}
				head, tail, _ := strings.Cut(s, "\n")
				if tail == "" {
					done = true
				}
				head = strings.TrimSpace(head)
				if head == "" {
					continue
				}
				var b strings.Builder
				stripMarkdownStyles(&b, []byte(head))
				return b.String()
			}
		},
	}

	if !response.IsDir {
		response.Content, err = readFile(nbrew.FS, path.Join(sitePrefix, response.Path))
		if err != nil {
			logger.Error(err.Error())
			internalServerError(w, r, err)
			return
		}
		modTime := fileInfo.ModTime()
		response.ModTime = &modTime
		accept, _, _ := mime.ParseMediaType(r.Header.Get("Accept"))
		if accept == "application/json" {
			w.Header().Set("Content-Type", "application/json")
			response.Alerts = nil
			b, err := json.Marshal(&response)
			if err != nil {
				logger.Error(err.Error())
				internalServerError(w, r, err)
				return
			}
			w.Write(b)
			return
		}
		tmpl, err := template.New("filesystem_file.html").Funcs(funcMap).ParseFS(rootFS, "filesystem_file.html")
		if err != nil {
			logger.Error(err.Error())
			internalServerError(w, r, err)
			return
		}
		buf := bufPool.Get().(*bytes.Buffer)
		buf.Reset()
		defer bufPool.Put(buf)
		err = tmpl.Execute(buf, &response)
		if err != nil {
			logger.Error(err.Error())
			internalServerError(w, r, err)
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
			internalServerError(w, r, err)
			return
		}
		defer cursor.Close()
		for cursor.Next() {
			siteName, err := cursor.Result()
			if err != nil {
				logger.Error(err.Error())
				internalServerError(w, r, err)
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
			internalServerError(w, r, err)
			return
		}
	}

	var folders []Entry     // folders
	var siteFolders []Entry // we want to display site folders after normal folders, so we aggregate them separately
	var files []Entry       // files
	dirEntries, err := nbrew.FS.ReadDir(path.Join(sitePrefix, response.Path))
	if err != nil {
		logger.Error(err.Error())
		internalServerError(w, r, err)
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
						internalServerError(w, r, err)
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
					if nbrew.DB == nil || authorizedSitePrefixes[entry.Name] {
						siteFolders = append(siteFolders, entry)
					}
				}
			}
			continue
		}
		// TODO: Don't nest more than one level for notes and posts.
		// TODO: Don't show anything other than .md and .txt files for notes and posts.
		if entry.IsDir {
			if head == "notes" || head == "posts" {
				if strings.Count(response.Path, "/") > 0 {
					continue
				}
			}
			folders = append(folders, entry)
			continue
		}
		if head == "notes" || head == "posts" {
			ext := path.Ext(entry.Name)
			if ext != ".md" && ext != ".txt" {
				continue
			}
		}
		fileInfo, err := dirEntry.Info()
		if err != nil {
			logger.Error(err.Error(), slog.String("name", entry.Name))
			internalServerError(w, r, err)
			return
		}
		modTime := fileInfo.ModTime()
		entry.ModTime = &modTime
		entry.Size = fileInfo.Size()
		if head == "notes" || head == "posts" {
			file, err := nbrew.FS.Open(path.Join(sitePrefix, response.Path, entry.Name))
			if err != nil {
				logger.Error(err.Error(), slog.String("name", entry.Name))
				internalServerError(w, r, err)
				return
			}
			entry.Title, entry.Preview = getTitleAndPreview(file)
		}
		files = append(files, entry)
	}
	switch response.Sort {
	case "name", "created":
		if response.Order == "desc" {
			for i := len(files)/2 - 1; i >= 0; i-- {
				opp := len(files) - 1 - i
				files[i], files[opp] = files[opp], files[i]
			}
		}
	case "edited":
		sort.Slice(files, func(i, j int) bool {
			t1, t2 := files[i].ModTime, files[j].ModTime
			if t1.Equal(*t2) {
				return false
			}
			less := t1.Before(*t2)
			if response.Order == "asc" {
				return less
			}
			return !less
		})
	case "title":
		if head == "notes" || head == "posts" {
			sort.Slice(files, func(i, j int) bool {
				title1, title2 := files[i].Title, files[j].Title
				if title1 == title2 {
					return false
				}
				less := title1 < title2
				if response.Order == "asc" {
					return less
				}
				return !less
			})
		}
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
			internalServerError(w, r, err)
			return
		}
		w.Write(b)
		return
	}
	tmpl, err := template.New("filesystem_folder.html").Funcs(funcMap).ParseFS(rootFS, "filesystem_folder.html")
	if err != nil {
		logger.Error(err.Error())
		internalServerError(w, r, err)
		return
	}
	buf := bufPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer bufPool.Put(buf)
	err = tmpl.Execute(buf, &response)
	if err != nil {
		logger.Error(err.Error())
		internalServerError(w, r, err)
		return
	}
	w.Header().Add("Content-Security-Policy", defaultContentSecurityPolicy)
	buf.WriteTo(w)
}
