package nb6

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"database/sql"
	"embed"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"sort"
	"strings"
	"sync"
	"text/template/parse"
	"time"

	"github.com/bokwoon95/sq"
	"github.com/oklog/ulid/v2"
	"golang.org/x/crypto/blake2b"
	"golang.org/x/exp/slices"
	"golang.org/x/exp/slog"
)

//go:embed html static
var embedFS embed.FS

var rootFS fs.FS = os.DirFS(".")

var bufPool = sync.Pool{
	New: func() any { return &bytes.Buffer{} },
}

var gzipPool = sync.Pool{
	New: func() any {
		// Use compression level 4 for best balance between space and
		// performance.
		// https://blog.klauspost.com/gzip-performance-for-go-webservers/
		gzipWriter, _ := gzip.NewWriterLevel(nil, 4)
		return gzipWriter
	},
}

type contextKey struct{}

var loggerKey = &contextKey{}

// Notebrew represents a notebrew instance.
type Notebrew struct {
	// FS is the file system associated with the notebrew instance.
	FS FS

	// DB is the database associated with the notebrew instance.
	DB *sql.DB

	// Dialect is dialect of the database. Only sqlite, postgres and mysql
	// databases are supported.
	Dialect string

	Scheme string // http:// | https://

	AdminDomain string // localhost:6444, example.com

	ContentDomain string // localhost:6444, example.com

	MultisiteMode string // subdomain | subdirectory

	// ErrorCode translates a database error into an dialect-specific error
	// code. If the error is not a database error or if no underlying
	// implementation is provided, ErrorCode returns an empty string.
	ErrorCode func(error) string

	CompressGeneratedHTML bool
}

func (nbrew *Notebrew) createFile(w http.ResponseWriter, r *http.Request, sitePrefix string) {
	type Request struct {
		ParentFolder string `json:"parent_folder,omitempty"`
		Name         string `json:"name,omitempty"`
	}
	type Response struct {
		ParentFolder       string   `json:"parent_folder,omitempty"`
		ParentFolderErrors []string `json:"parent_folder_errors,omitempty"`
		Name               string   `json:"name,omitempty"`
		NameErrors         []string `json:"name_errors,omitempty"`
		Error              string   `json:"error,omitempty"`
		AlreadyExists      string   `json:"already_exists,omitempty"`
	}

	logger, ok := r.Context().Value(loggerKey).(*slog.Logger)
	if !ok {
		logger = slog.Default()
	}
	logger = logger.With(
		slog.String("method", r.Method),
		slog.String("url", r.URL.String()),
		slog.String("sitePrefix", sitePrefix),
	)
	r = r.WithContext(context.WithValue(r.Context(), loggerKey, logger))

	switch r.Method {
	case "GET":
		err := r.ParseForm()
		if err != nil {
			http.Error(w, fmt.Sprintf("400 Bad Request: %s", err), http.StatusBadRequest)
			return
		}
		var response Response
		ok, err := nbrew.getSession(r, "flash_session", &response)
		if err != nil {
			logger.Error(err.Error())
		}
		if !ok {
			response.ParentFolder = r.Form.Get("parent_folder")
			response.Name = r.Form.Get("name")
		}
		if response.ParentFolder != "" {
			response.ParentFolder = strings.Trim(path.Clean(response.ParentFolder), "/")
		}
		nbrew.clearSession(w, r, "flash_session")
		tmpl, err := template.ParseFS(rootFS, "html/create_file.html")
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
	case "POST":
		writeResponse := func(w http.ResponseWriter, r *http.Request, response Response) {
			accept, _, _ := mime.ParseMediaType(r.Header.Get("Accept"))
			if accept == "application/json" {
				b, err := json.Marshal(&response)
				if err != nil {
					logger.Error(err.Error())
					http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
					return
				}
				w.Write(b)
				return
			}
			if len(response.ParentFolderErrors) == 0 && len(response.NameErrors) == 0 && response.Error == "" && response.AlreadyExists == "" {
				http.Redirect(w, r, "/"+path.Join("admin", sitePrefix, response.ParentFolder, response.Name), http.StatusFound)
				return
			}
			err := nbrew.setSession(w, r, &response, &http.Cookie{
				Path:     r.URL.Path,
				Name:     "flash_session",
				Secure:   nbrew.Scheme == "https://",
				HttpOnly: true,
				SameSite: http.SameSiteLaxMode,
			})
			if err != nil {
				logger.Error(err.Error())
				http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
				return
			}
			http.Redirect(w, r, r.URL.String(), http.StatusFound)
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
				http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
				return
			}
		case "application/x-www-form-urlencoded":
			err := r.ParseForm()
			if err != nil {
				http.Error(w, fmt.Sprintf("400 Bad Request: %s", err), http.StatusBadRequest)
				return
			}
			request.ParentFolder = r.Form.Get("parent_folder")
			request.Name = r.Form.Get("name")
		default:
			http.Error(w, "415 Unsupported Media Type", http.StatusUnsupportedMediaType)
			return
		}

		response := Response{
			ParentFolder: request.ParentFolder,
			Name:         request.Name,
		}
		if response.ParentFolder != "" {
			response.ParentFolder = strings.Trim(path.Clean(response.ParentFolder), "/")
		}
		head, tail, _ := strings.Cut(response.ParentFolder, "/")

		if head != "posts" && head != "notes" && head != "pages" && head != "templates" && head != "assets" {
			response.ParentFolderErrors = append(response.ParentFolderErrors, "parent folder has to start with posts, notes, pages, templates or assets")
		} else if (head == "posts" || head == "notes") && strings.Contains(tail, "/") {
			response.ParentFolderErrors = append(response.ParentFolderErrors, "not allowed to use this parent folder")
		}

		if (head == "posts" || head == "notes") && response.Name == "" {
			response.Name = strings.ToLower(ulid.Make().String()) + ".md"
		}

		if response.Name == "" {
			response.NameErrors = append(response.NameErrors, "cannot be empty")
		} else {
			response.NameErrors = validateName(response.NameErrors, response.Name)
			switch head {
			case "posts", "notes":
				if path.Ext(response.Name) != ".md" {
					response.NameErrors = append(response.NameErrors, "invalid extension (must end in .md)")
				}
			case "pages", "templates":
				if path.Ext(response.Name) != ".html" {
					response.NameErrors = append(response.NameErrors, "invalid extension (must end in .html)")
				}
			case "assets":
				ext := path.Ext(response.Name)
				if ext == ".gz" {
					ext = path.Ext(strings.TrimSuffix(response.Name, ext))
				}
				allowedExts := []string{
					".html", ".css", ".js", ".md", ".txt",
					".jpeg", ".jpg", ".png", ".gif", ".svg", ".ico",
					".eof", ".ttf", ".woff", ".woff2",
					".csv", ".tsv", ".json", ".xml", ".toml", ".yaml", ".yml",
				}
				if !slices.Contains(allowedExts, ext) {
					response.NameErrors = append(response.NameErrors, fmt.Sprintf("invalid extension (must be one of: %s)", strings.Join(allowedExts, ", ")))
				}
			}
		}

		if len(response.ParentFolderErrors) > 0 || len(response.NameErrors) > 0 {
			writeResponse(w, r, response)
			return
		}

		_, err := fs.Stat(nbrew.FS, path.Join(sitePrefix, response.ParentFolder))
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				logger.Error(err.Error())
				http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
				return
			}
			response.ParentFolderErrors = append(response.ParentFolderErrors, "folder does not exist")
			writeResponse(w, r, response)
			return
		}

		_, err = fs.Stat(nbrew.FS, path.Join(sitePrefix, response.ParentFolder, response.Name))
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			logger.Error(err.Error())
			http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
			return
		}
		if err == nil {
			response.AlreadyExists = "/" + path.Join("admin", sitePrefix, response.ParentFolder, response.Name)
			writeResponse(w, r, response)
			return
		}

		writer, err := nbrew.FS.OpenWriter(path.Join(sitePrefix, response.ParentFolder, response.Name))
		if err != nil {
			logger.Error(err.Error())
			http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
			return
		}
		err = writer.Close()
		if err != nil {
			logger.Error(err.Error())
			http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
			return
		}
		writeResponse(w, r, response)
	default:
		http.Error(w, "405 Method Not Allowed", http.StatusMethodNotAllowed)
	}
}

func (nbrew *Notebrew) notFound(w http.ResponseWriter, r *http.Request, sitePrefix string) {
	if r.Method == "GET" {
		// TODO: search the user's 400.html template and render that if found.
		http.Error(w, "404 Not Found", http.StatusNotFound)
		return
	}
	http.Error(w, "404 Not Found", http.StatusNotFound)
}

func (nbrew *Notebrew) setSession(w http.ResponseWriter, r *http.Request, v any, cookie *http.Cookie) error {
	dataBytes, ok := v.([]byte)
	if !ok {
		var err error
		dataBytes, err = json.Marshal(v)
		if err != nil {
			return fmt.Errorf("marshaling JSON: %w", err)
		}
	}
	if nbrew.DB == nil {
		cookie.Value = base64.URLEncoding.EncodeToString(dataBytes)
	} else {
		var sessionToken [8 + 16]byte
		binary.BigEndian.PutUint64(sessionToken[:8], uint64(time.Now().Unix()))
		_, err := rand.Read(sessionToken[8:])
		if err != nil {
			return fmt.Errorf("reading rand: %w", err)
		}
		var sessionTokenHash [8 + blake2b.Size256]byte
		checksum := blake2b.Sum256([]byte(sessionToken[8:]))
		copy(sessionTokenHash[:8], sessionToken[:8])
		copy(sessionTokenHash[8:], checksum[:])
		_, err = sq.ExecContext(r.Context(), nbrew.DB, sq.CustomQuery{
			Dialect: nbrew.Dialect,
			Format:  "INSERT INTO sessions (session_token_hash, data) VALUES ({sessionTokenHash}, {data})",
			Values: []any{
				sq.BytesParam("sessionTokenHash", sessionTokenHash[:]),
				sq.BytesParam("data", dataBytes),
			},
		})
		if err != nil {
			return fmt.Errorf("saving session: %w", err)
		}
		cookie.Value = strings.TrimLeft(hex.EncodeToString(sessionToken[:]), "0")
	}
	http.SetCookie(w, cookie)
	return nil
}

func (nbrew *Notebrew) getSession(r *http.Request, name string, v any) (ok bool, err error) {
	cookie, _ := r.Cookie(name)
	if cookie == nil {
		return false, nil
	}
	var dataBytes []byte
	if nbrew.DB == nil {
		dataBytes, err = base64.URLEncoding.DecodeString(cookie.Value)
		if err != nil {
			return false, nil
		}
	} else {
		sessionToken, err := hex.DecodeString(fmt.Sprintf("%048s", cookie.Value))
		if err != nil {
			return false, nil
		}
		var sessionTokenHash [8 + blake2b.Size256]byte
		checksum := blake2b.Sum256([]byte(sessionToken[8:]))
		copy(sessionTokenHash[:8], sessionToken[:8])
		copy(sessionTokenHash[8:], checksum[:])
		createdAt := time.Unix(int64(binary.BigEndian.Uint64(sessionTokenHash[:8])), 0)
		if time.Now().Sub(createdAt) > 5*time.Minute {
			return false, nil
		}
		dataBytes, err = sq.FetchOneContext(r.Context(), nbrew.DB, sq.CustomQuery{
			Dialect: nbrew.Dialect,
			Format:  "SELECT {*} FROM sessions WHERE session_token_hash = {sessionTokenHash}",
			Values: []any{
				sq.BytesParam("sessionTokenHash", sessionTokenHash[:]),
			},
		}, func(row *sq.Row) []byte {
			return row.Bytes("data")
		})
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return false, nil
			}
			return false, err
		}
	}
	if ptr, ok := v.(*[]byte); ok {
		*ptr = dataBytes
		return true, nil
	}
	err = json.Unmarshal(dataBytes, v)
	if err != nil {
		return false, err
	}
	return true, nil
}

func (nbrew *Notebrew) clearSession(w http.ResponseWriter, r *http.Request, name string) {
	http.SetCookie(w, &http.Cookie{
		Name:   name,
		Value:  "",
		MaxAge: -1,
	})
	if nbrew.DB == nil {
		return
	}
	cookie, _ := r.Cookie(name)
	if cookie == nil {
		return
	}
	sessionToken, err := hex.DecodeString(fmt.Sprintf("%048s", cookie.Value))
	if err != nil {
		return
	}
	var sessionTokenHash [8 + blake2b.Size256]byte
	checksum := blake2b.Sum256([]byte(sessionToken[8:]))
	copy(sessionTokenHash[:8], sessionToken[:8])
	copy(sessionTokenHash[8:], checksum[:])
	_, err = sq.ExecContext(r.Context(), nbrew.DB, sq.CustomQuery{
		Dialect: nbrew.Dialect,
		Format:  "DELETE FROM sessions WHERE session_token_hash = {sessionTokenHash}",
		Values: []any{
			sq.BytesParam("sessionTokenHash", sessionTokenHash[:]),
		},
	})
	if err != nil {
		logger, ok := r.Context().Value(loggerKey).(*slog.Logger)
		if !ok {
			logger = slog.Default()
		}
		logger.Error(err.Error())
	}
}

func readFile(fsys fs.FS, name string) (string, error) {
	file, err := fsys.Open(name)
	if err != nil {
		return "", err
	}
	defer file.Close()
	var size int
	if info, err := file.Stat(); err == nil {
		size64 := info.Size()
		if int64(int(size64)) == size64 {
			size = int(size64)
		}
	}
	var b strings.Builder
	b.Grow(size)
	_, err = io.Copy(&b, file)
	if err != nil {
		return "", err
	}
	return b.String(), nil
}

var uppercaseCharSet = map[rune]struct{}{
	'A': {}, 'B': {}, 'C': {}, 'D': {}, 'E': {}, 'F': {}, 'G': {}, 'H': {}, 'I': {},
	'J': {}, 'K': {}, 'L': {}, 'M': {}, 'N': {}, 'O': {}, 'P': {}, 'Q': {}, 'R': {},
	'S': {}, 'T': {}, 'U': {}, 'V': {}, 'W': {}, 'X': {}, 'Y': {}, 'Z': {},
}

var forbiddenCharSet = map[rune]struct{}{
	' ': {}, '!': {}, '"': {}, '#': {}, '$': {}, '%': {}, '&': {}, '\'': {},
	'(': {}, ')': {}, '*': {}, '+': {}, ',': {}, '/': {}, ':': {}, ';': {},
	'<': {}, '>': {}, '=': {}, '?': {}, '[': {}, ']': {}, '\\': {}, '^': {},
	'`': {}, '{': {}, '}': {}, '|': {}, '~': {},
}

var forbiddenNameSet = map[string]struct{}{
	"con": {}, "prn": {}, "aux": {}, "nul": {}, "com1": {}, "com2": {},
	"com3": {}, "com4": {}, "com5": {}, "com6": {}, "com7": {}, "com8": {},
	"com9": {}, "lpt1": {}, "lpt2": {}, "lpt3": {}, "lpt4": {}, "lpt5": {},
	"lpt6": {}, "lpt7": {}, "lpt8": {}, "lpt9": {},
}

func validateName(errmsgs []string, name string) []string {
	var forbiddenChars strings.Builder
	hasUppercaseChar := false
	writtenChar := make(map[rune]struct{})
	for _, char := range name {
		if _, ok := uppercaseCharSet[char]; ok {
			hasUppercaseChar = true
		}
		if _, ok := forbiddenCharSet[char]; ok {
			if _, ok := writtenChar[char]; !ok {
				writtenChar[char] = struct{}{}
				forbiddenChars.WriteRune(char)
			}
		}
	}
	if hasUppercaseChar {
		errmsgs = append(errmsgs, "no uppercase letters [A-Z] allowed")
	}
	if forbiddenChars.Len() > 0 {
		errmsgs = append(errmsgs, "forbidden characters: "+forbiddenChars.String())
	}
	if len(name) > 0 && name[len(name)-1] == '.' {
		errmsgs = append(errmsgs, "cannot end in dot")
	}
	if _, ok := forbiddenNameSet[strings.ToLower(name)]; ok {
		errmsgs = append(errmsgs, "forbidden name")
	}
	return errmsgs
}

func getAuthenticationTokenHash(r *http.Request) []byte {
	cookie, _ := r.Cookie("authentication_token")
	if cookie == nil || cookie.Value == "" {
		return nil
	}
	authenticationToken, err := hex.DecodeString(fmt.Sprintf("%048s", cookie.Value))
	if err != nil {
		return nil
	}
	var authenticationTokenHash [8 + blake2b.Size256]byte
	checksum := blake2b.Sum256([]byte(authenticationToken[8:]))
	copy(authenticationTokenHash[:8], authenticationToken[:8])
	copy(authenticationTokenHash[8:], checksum[:])
	return authenticationTokenHash[:]
}

func ParseTemplate(fsys fs.FS, funcMap template.FuncMap, text string) (*template.Template, error) {
	primaryTemplate, err := template.New("").Funcs(funcMap).Parse(text)
	if err != nil {
		return nil, err
	}
	primaryTemplates := primaryTemplate.Templates()
	sort.SliceStable(primaryTemplates, func(i, j int) bool {
		return primaryTemplates[j].Name() < primaryTemplates[i].Name()
	})
	for _, primaryTemplate := range primaryTemplates {
		name := primaryTemplate.Name()
		if strings.HasSuffix(name, ".html") {
			return nil, fmt.Errorf("define %q: defined template name cannot end in .html", name)
		}
	}
	var errmsgs []string
	var currentNode parse.Node
	var nodeStack []parse.Node
	var currentTemplate *template.Template
	templateStack := primaryTemplates
	finalTemplate := template.New("").Funcs(funcMap)
	visited := make(map[string]struct{})
	for len(templateStack) > 0 {
		currentTemplate, templateStack = templateStack[len(templateStack)-1], templateStack[:len(templateStack)-1]
		if currentTemplate.Tree == nil {
			continue
		}
		if cap(nodeStack) < len(currentTemplate.Tree.Root.Nodes) {
			nodeStack = make([]parse.Node, 0, len(currentTemplate.Tree.Root.Nodes))
		}
		for i := len(currentTemplate.Tree.Root.Nodes) - 1; i >= 0; i-- {
			nodeStack = append(nodeStack, currentTemplate.Tree.Root.Nodes[i])
		}
		for len(nodeStack) > 0 {
			currentNode, nodeStack = nodeStack[len(nodeStack)-1], nodeStack[:len(nodeStack)-1]
			switch node := currentNode.(type) {
			case *parse.ListNode:
				if node == nil {
					continue
				}
				for i := len(node.Nodes) - 1; i >= 0; i-- {
					nodeStack = append(nodeStack, node.Nodes[i])
				}
			case *parse.BranchNode:
				nodeStack = append(nodeStack, node.ElseList, node.List)
			case *parse.RangeNode:
				nodeStack = append(nodeStack, node.ElseList, node.List)
			case *parse.TemplateNode:
				if !strings.HasSuffix(node.Name, ".html") {
					continue
				}
				filename := node.Name
				if _, ok := visited[filename]; ok {
					continue
				}
				visited[filename] = struct{}{}
				file, err := fsys.Open(filename)
				if errors.Is(err, fs.ErrNotExist) {
					errmsgs = append(errmsgs, fmt.Sprintf("%s: %s does not exist", currentTemplate.Name(), node.String()))
					continue
				}
				if err != nil {
					return nil, err
				}
				fileinfo, err := file.Stat()
				if err != nil {
					return nil, err
				}
				var b strings.Builder
				b.Grow(int(fileinfo.Size()))
				_, err = io.Copy(&b, file)
				if err != nil {
					return nil, err
				}
				file.Close()
				text := b.String()
				newTemplate, err := template.New(filename).Funcs(funcMap).Parse(text)
				if err != nil {
					return nil, err
				}
				newTemplates := newTemplate.Templates()
				sort.SliceStable(newTemplates, func(i, j int) bool {
					return newTemplates[j].Name() < newTemplates[i].Name()
				})
				for _, newTemplate := range newTemplates {
					name := newTemplate.Name()
					if name != filename && strings.HasSuffix(name, ".html") {
						return nil, fmt.Errorf("define %q: defined template name cannot end in .html", name)
					}
					_, err = finalTemplate.AddParseTree(name, newTemplate.Tree)
					if err != nil {
						return nil, err
					}
					templateStack = append(templateStack, newTemplate)
				}
			}
		}
	}
	if len(errmsgs) > 0 {
		return nil, fmt.Errorf("invalid template references:\n" + strings.Join(errmsgs, "\n"))
	}
	for _, primaryTemplate := range primaryTemplates {
		_, err = finalTemplate.AddParseTree(primaryTemplate.Name(), primaryTemplate.Tree)
		if err != nil {
			return nil, err
		}
	}
	return finalTemplate, nil
}

func (nbrew *Notebrew) IsKeyViolation(err error) bool {
	if err == nil || nbrew.ErrorCode == nil {
		return false
	}
	errcode := nbrew.ErrorCode(err)
	switch nbrew.Dialect {
	case "sqlite":
		return errcode == "1555" || errcode == "2067" // SQLITE_CONSTRAINT_PRIMARYKEY, SQLITE_CONSTRAINT_UNIQUE
	case "postgres":
		return errcode == "23505" // unique_violation
	case "mysql":
		return errcode == "1062" // ER_DUP_ENTRY
	case "sqlserver":
		return errcode == "2627"
	default:
		return false
	}
}

func (nbrew *Notebrew) IsForeignKeyViolation(err error) bool {
	if err == nil || nbrew.ErrorCode == nil {
		return false
	}
	errcode := nbrew.ErrorCode(err)
	switch nbrew.Dialect {
	case "sqlite":
		return errcode == "787" //  SQLITE_CONSTRAINT_FOREIGNKEY
	case "postgres":
		return errcode == "23503" // foreign_key_violation
	case "mysql":
		return errcode == "1216" // ER_NO_REFERENCED_ROW
	case "sqlserver":
		return errcode == "547"
	default:
		return false
	}
}
