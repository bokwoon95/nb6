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
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/exp/slog"
)

func (nbrew *Notebrew) delet(w http.ResponseWriter, r *http.Request, username, sitePrefix string) {
	type Request struct {
		Folder string   `json:"folder,omitempty"`
		Names  []string `json:"names,omitempty"`
	}
	type Response struct {
		Folder  string   `json:"folder,omitempty"`
		Deleted []string `json:"deleted,omitempty"`
		Errors  []string `json:"errors,omitempty"`
	}
	type Entry struct {
		Name    string    `json:"name,omitempty"`
		IsDir   bool      `json:"is_dir,omitempty"`
		Size    int64     `json:"size,omitempty"`
		ModTime time.Time `json:"mod_time,omitempty"`
	}
	type TemplateData struct {
		Folder  string  `json:"folder,omitempty"`
		Entries []Entry `json:"entries,omitempty"`
	}

	logger, ok := r.Context().Value(loggerKey).(*slog.Logger)
	if !ok {
		logger = slog.Default()
	}

	isValidFolder := func(folder string) bool {
		head, _, _ := strings.Cut(folder, "/")
		switch head {
		case "notes", "pages", "posts", "themes":
			fileInfo, err := fs.Stat(nbrew.FS, path.Join(sitePrefix, folder))
			if err != nil {
				return false
			}
			if fileInfo.IsDir() {
				return true
			}
		}
		return false
	}

	switch r.Method {
	case "GET":
		err := r.ParseForm()
		if err != nil {
			http.Error(w, fmt.Sprintf("400 Bad Request: %s", err), http.StatusBadRequest)
			return
		}
		var templateData TemplateData
		folder := path.Clean(strings.Trim(r.Form.Get("folder"), "/"))
		if isValidFolder(folder) {
			templateData.Folder = folder
			seen := make(map[string]bool)
			for _, name := range r.Form["name"] {
				name = filepath.ToSlash(name)
				if strings.Contains(name, "/") {
					continue
				}
				if seen[name] {
					continue
				}
				seen[name] = true
				fileInfo, err := fs.Stat(nbrew.FS, path.Join(sitePrefix, templateData.Folder, name))
				if err != nil {
					if errors.Is(err, fs.ErrNotExist) {
						continue
					}
					logger.Error(err.Error())
					http.Error(w, messageInternalServerError, http.StatusInternalServerError)
					return
				}
				templateData.Entries = append(templateData.Entries, Entry{
					Name:    fileInfo.Name(),
					IsDir:   fileInfo.IsDir(),
					Size:    fileInfo.Size(),
					ModTime: fileInfo.ModTime(),
				})
			}
		}

		funcMap := map[string]any{
			"join":       path.Join,
			"referer":    func() string { return r.Referer() },
			"sitePrefix": func() string { return sitePrefix },
		}
		tmpl, err := template.New("delete.html").Funcs(funcMap).ParseFS(rootFS, "html/delete.html")
		if err != nil {
			logger.Error(err.Error())
			http.Error(w, messageInternalServerError, http.StatusInternalServerError)
			return
		}
		buf := bufPool.Get().(*bytes.Buffer)
		buf.Reset()
		defer bufPool.Put(buf)
		err = tmpl.Execute(buf, &templateData)
		if err != nil {
			logger.Error(err.Error())
			http.Error(w, messageInternalServerError, http.StatusInternalServerError)
			return
		}
		w.Header().Add("Content-Security-Policy", defaultContentSecurityPolicy)
		buf.WriteTo(w)
	case "POST":
		writeResponse := func(w http.ResponseWriter, r *http.Request, response Response) {
			accept, _, _ := mime.ParseMediaType(r.Header.Get("Accept"))
			if accept == "application/json" {
				b, err := json.Marshal(&response)
				if err != nil {
					logger.Error(err.Error())
					http.Error(w, messageInternalServerError, http.StatusInternalServerError)
					return
				}
				w.Write(b)
				return
			}
			if len(response.Deleted) > 0 {
				msg := "1 item deleted"
				if len(response.Deleted) > 1 {
					msg = fmt.Sprintf("%d items deleted", len(response.Deleted))
				}
				err := nbrew.setSession(w, r, "flash", map[string]any{
					"alerts": url.Values{
						"success": []string{msg},
					},
				})
				if err != nil {
					logger.Error(err.Error())
				}
			}
			http.Redirect(w, r, nbrew.Scheme+nbrew.AdminDomain+"/"+path.Join("admin", sitePrefix, response.Folder)+"/", http.StatusFound)
		}

		var request Request
		contentType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		switch contentType {
		case "application/json":
			err := json.NewDecoder(r.Body).Decode(&request)
			if err != nil {
				var syntaxErr *json.SyntaxError
				if errors.As(err, &syntaxErr) {
					http.Error(w, "400 Bad Request: invalid JSON", http.StatusBadRequest)
					return
				}
				logger.Error(err.Error())
				http.Error(w, messageInternalServerError, http.StatusInternalServerError)
				return
			}
		case "application/x-www-form-urlencoded":
			err := r.ParseForm()
			if err != nil {
				http.Error(w, fmt.Sprintf("400 Bad Request: %s", err), http.StatusBadRequest)
				return
			}
			request.Folder = path.Clean(strings.Trim(r.Form.Get("folder"), "/"))
			request.Names = r.Form["name"]
		default:
			http.Error(w, "415 Unsupported Media Type", http.StatusUnsupportedMediaType)
			return
		}

		response := Response{}
		if !isValidFolder(request.Folder) {
			response.Errors = append(response.Errors, fmt.Sprintf("invalid folder %s", request.Folder))
			writeResponse(w, r, response)
			return
		}
		response.Folder = request.Folder
		seen := make(map[string]bool)
		for _, name := range request.Names {
			if strings.Contains(name, "/") {
				continue
			}
			if seen[name] {
				continue
			}
			seen[name] = true
			err := remove(nbrew.FS, path.Join(sitePrefix, request.Folder, name))
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					continue
				}
				response.Errors = append(response.Errors, fmt.Sprintf("%s: %v", name, err))
			} else {
				response.Deleted = append(response.Deleted, name)
			}
		}
		writeResponse(w, r, response)
	default:
		http.Error(w, "405 Method Not Allowed", http.StatusMethodNotAllowed)
	}
}

// remove removes the root item from the FS (whether it is a file or a
// directory).
//
// TODO: remove should only move file to the recycle bin, and the user is given
// an link to immediately undo what has been done (which simply takes them to
// /admin/{sitePrefix}/recover/).
func remove(fsys FS, root string) error {
	type Item struct {
		Path             string // relative to root
		IsFile           bool   // whether item is file or directory
		MarkedForRemoval bool   // if true, remove item unconditionally
	}
	fileInfo, err := fs.Stat(fsys, root)
	if err != nil {
		return err
	}
	// If root is a file, we can remove it immediately and return.
	if !fileInfo.IsDir() {
		return fsys.Remove(root)
	}
	// If root is an empty directoy, we can remove it immediately and return.
	dirEntries, err := fsys.ReadDir(root)
	if len(dirEntries) == 0 {
		return fsys.Remove(root)
	}
	// If the filesystem supports RemoveAll(), we can call that instead and
	// return.
	if fsys, ok := fsys.(interface{ RemoveAll(name string) error }); ok {
		return fsys.RemoveAll(root)
	}
	// Otherwise, we need to recursively delete its child items one by one.
	var item Item
	items := make([]Item, 0, len(dirEntries))
	for i := len(dirEntries) - 1; i >= 0; i-- {
		dirEntry := dirEntries[i]
		items = append(items, Item{
			Path:   path.Join(root, dirEntry.Name()),
			IsFile: !dirEntry.IsDir(),
		})
	}
	for len(items) > 0 {
		// Pop item from stack.
		item, items = items[len(items)-1], items[:len(items)-1]
		// If item has been marked for removal or it is a file, we can remove
		// it immediately.
		if item.MarkedForRemoval || item.IsFile {
			err = fsys.Remove(path.Join(root, item.Path))
			if err != nil {
				return err
			}
			continue
		}
		// Mark directory item for removal and put it back in the stack (when
		// we get back to it, its child items would already have been removed).
		item.MarkedForRemoval = true
		items = append(items, item)
		// Push directory item's child items onto the stack.
		dirEntries, err := fsys.ReadDir(path.Join(root, item.Path))
		if err != nil {
			return err
		}
		for i := len(dirEntries) - 1; i >= 0; i-- {
			dirEntry := dirEntries[i]
			items = append(items, Item{
				Path:   path.Join(root, dirEntry.Name()),
				IsFile: !dirEntry.IsDir(),
			})
		}
	}
	return nil
}

func (nbrew *Notebrew) deletOld(w http.ResponseWriter, r *http.Request, username, sitePrefix string) {
	type Request struct {
		Path      string `json:"path,omitempty"`
		Recursive bool   `json:"recursive,omitempty"`
	}
	type Response struct {
		Path             string     `json:"path,omitempty"`
		IsNonEmptyFolder bool       `json:"is_non_empty_folder,omitempty"`
		Recursive        bool       `json:"recursive,omitempty"`
		Errors           url.Values `json:"errors,omitempty"`
	}

	logger, ok := r.Context().Value(loggerKey).(*slog.Logger)
	if !ok {
		logger = slog.Default()
	}

	switch r.Method {
	case "GET":
		err := r.ParseForm()
		if err != nil {
			http.Error(w, fmt.Sprintf("400 Bad Request: %s", err), http.StatusBadRequest)
			return
		}
		var response Response
		ok, err := nbrew.getSession(r, "flash", &response)
		if err != nil {
			logger.Error(err.Error())
		} else if !ok {
			response.Path = r.Form.Get("path")
			response.Recursive, _ = strconv.ParseBool(r.Form.Get("recursive"))
		}
		if response.Path != "" {
			response.Path = strings.Trim(path.Clean(response.Path), "/")
		}
		nbrew.clearSession(w, r, "flash")

		tmpl, err := template.ParseFS(rootFS, "html/delete.html")
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
	case "POST":
		writeResponse := func(w http.ResponseWriter, r *http.Request, response Response) {
			accept, _, _ := mime.ParseMediaType(r.Header.Get("Accept"))
			if accept == "application/json" {
				if len(response.Errors) == 0 && response.IsNonEmptyFolder && !response.Recursive {
					response.Errors.Set("", "cannot delete non-empty folder unless \"recursive\" property is set to true")
				}
				b, err := json.Marshal(&response)
				if err != nil {
					logger.Error(err.Error())
					http.Error(w, messageInternalServerError, http.StatusInternalServerError)
					return
				}
				w.Write(b)
				return
			}
			if len(response.Errors) > 0 || (response.IsNonEmptyFolder && !response.Recursive) {
				err := nbrew.setSession(w, r, "flash", &response)
				if err != nil {
					logger.Error(err.Error())
					http.Error(w, messageInternalServerError, http.StatusInternalServerError)
					return
				}
				http.Redirect(w, r, r.URL.String(), http.StatusFound)
				return
			}
			http.Redirect(w, r, nbrew.Scheme+nbrew.AdminDomain+"/"+path.Join("admin", sitePrefix, path.Dir(response.Path))+"/", http.StatusFound)
		}

		var request Request
		contentType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		switch contentType {
		case "application/json":
			err := json.NewDecoder(r.Body).Decode(&request)
			if err != nil {
				var syntaxErr *json.SyntaxError
				if errors.As(err, &syntaxErr) {
					http.Error(w, "400 Bad Request: invalid JSON", http.StatusBadRequest)
					return
				}
				logger.Error(err.Error())
				http.Error(w, messageInternalServerError, http.StatusInternalServerError)
				return
			}
		case "application/x-www-form-urlencoded":
			err := r.ParseForm()
			if err != nil {
				http.Error(w, fmt.Sprintf("400 Bad Request: %s", err), http.StatusBadRequest)
				return
			}
			request.Path = r.Form.Get("path")
			request.Recursive, _ = strconv.ParseBool(r.Form.Get("recursive"))
		default:
			http.Error(w, "415 Unsupported Media Type", http.StatusUnsupportedMediaType)
			return
		}

		response := Response{
			Path:      request.Path,
			Recursive: request.Recursive,
			Errors:    make(url.Values),
		}
		if response.Path != "" {
			response.Path = strings.Trim(path.Clean(response.Path), "/")
		}
		filePath := path.Join(sitePrefix, response.Path)

		fileInfo, err := fs.Stat(nbrew.FS, filePath)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				response.Errors.Add("path", "file or folder does not exist")
				writeResponse(w, r, response)
				return
			}
			logger.Error(err.Error())
			http.Error(w, messageInternalServerError, http.StatusInternalServerError)
			return
		}

		if !fileInfo.IsDir() {
			err = nbrew.FS.Remove(filePath)
			if err != nil {
				logger.Error(err.Error())
				http.Error(w, messageInternalServerError, http.StatusInternalServerError)
				return
			}
			writeResponse(w, r, response)
			return
		}

		dirEntries, err := nbrew.FS.ReadDir(filePath)
		if err != nil {
			logger.Error(err.Error())
			http.Error(w, messageInternalServerError, http.StatusInternalServerError)
			return
		}
		if len(dirEntries) == 0 {
			err = nbrew.FS.Remove(filePath)
			if err != nil {
				logger.Error(err.Error())
				http.Error(w, messageInternalServerError, http.StatusInternalServerError)
				return
			}
			writeResponse(w, r, response)
			return
		}
		response.IsNonEmptyFolder = true
		if !response.Recursive {
			writeResponse(w, r, response)
			return
		}

		if fsys, ok := nbrew.FS.(interface{ RemoveAll(name string) error }); ok {
			err = fsys.RemoveAll(filePath)
			if err != nil {
				logger.Error(err.Error())
				http.Error(w, messageInternalServerError, http.StatusInternalServerError)
				return
			}
			writeResponse(w, r, response)
			return
		}

		type Item struct {
			RelativePath string // path relative to filePath
			IsFile       bool
			IsEmptyDir   bool
		}
		pushItems := func(items []Item, dir string, dirEntries []fs.DirEntry) []Item {
			for i := len(dirEntries) - 1; i >= 0; i-- {
				dirEntry := dirEntries[i]
				items = append(items, Item{
					RelativePath: path.Join(dir, dirEntry.Name()),
					IsFile:       !dirEntry.IsDir(),
				})
			}
			return items
		}
		var item Item
		items := pushItems(nil, "", dirEntries)
		for len(items) > 0 {
			item, items = items[len(items)-1], items[:len(items)-1]
			if item.IsFile || item.IsEmptyDir {
				err = nbrew.FS.Remove(path.Join(filePath, item.RelativePath))
				if err != nil {
					logger.Error(err.Error())
					http.Error(w, messageInternalServerError, http.StatusInternalServerError)
					return
				}
				continue
			}
			items = append(items, Item{
				RelativePath: item.RelativePath,
				IsEmptyDir:   true,
			})
			dirEntries, err := nbrew.FS.ReadDir(path.Join(filePath, item.RelativePath))
			if err != nil {
				logger.Error(err.Error())
				http.Error(w, messageInternalServerError, http.StatusInternalServerError)
				return
			}
			items = pushItems(items, item.RelativePath, dirEntries)
		}
		writeResponse(w, r, response)
	default:
		http.Error(w, "405 Method Not Allowed", http.StatusMethodNotAllowed)
	}
}
