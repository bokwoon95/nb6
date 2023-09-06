package nb6

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"database/sql"
	"embed"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"text/template/parse"
	"time"

	"github.com/bokwoon95/sq"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"golang.org/x/crypto/blake2b"
	"golang.org/x/exp/slog"
)

//go:embed static *.html
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

const defaultContentSecurityPolicy = "default-src 'none'; script-src 'self'; connect-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; base-uri 'self'; form-action 'self'"

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

	Stdout io.Writer

	CompressGeneratedHTML bool
}

func (nbrew *Notebrew) notFound(w http.ResponseWriter, r *http.Request, sitePrefix string) {
	if r.Method == "GET" {
		// TODO: search the user's 400.html template and render that if found.
		notFound(w, r)
		return
	}
	notFound(w, r)
}

func (nbrew *Notebrew) setSession(w http.ResponseWriter, r *http.Request, name string, value any) error {
	buf := bufPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer bufPool.Put(buf)
	b, ok := value.([]byte)
	if ok {
		buf.Write(b)
	} else {
		err := json.NewEncoder(buf).Encode(value)
		if err != nil {
			return fmt.Errorf("marshaling JSON: %w", err)
		}
	}
	cookie := &http.Cookie{
		Path:     "/",
		Name:     name,
		Secure:   nbrew.Scheme == "https://",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}
	if nbrew.DB == nil {
		cookie.Value = base64.URLEncoding.EncodeToString(buf.Bytes())
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
			Format:  "INSERT INTO session (session_token_hash, data) VALUES ({sessionTokenHash}, {data})",
			Values: []any{
				sq.BytesParam("sessionTokenHash", sessionTokenHash[:]),
				sq.StringParam("data", strings.TrimSpace(buf.String())),
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

func (nbrew *Notebrew) getSession(r *http.Request, name string, valuePtr any) (ok bool, err error) {
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
			Format:  "SELECT {*} FROM session WHERE session_token_hash = {sessionTokenHash}",
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
	if ptr, ok := valuePtr.(*[]byte); ok {
		*ptr = dataBytes
		return true, nil
	}
	err = json.Unmarshal(dataBytes, valuePtr)
	if err != nil {
		return false, err
	}
	return true, nil
}

func (nbrew *Notebrew) clearSession(w http.ResponseWriter, r *http.Request, name string) {
	http.SetCookie(w, &http.Cookie{
		Path:     "/",
		Name:     name,
		Value:    "0",
		MaxAge:   -1,
		Secure:   nbrew.Scheme == "https://",
		HttpOnly: true,
	})
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
		Format:  "DELETE FROM session WHERE session_token_hash = {sessionTokenHash}",
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

// readFile is like fs.ReadFile except it returns a string instead of a []byte
// (optimization using strings.Builder).
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

func validateName(name string) []string {
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
	var errmsgs []string
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
	var rawValue string
	header := r.Header.Get("Authorization")
	if strings.HasPrefix(header, "Notebrew ") {
		rawValue = strings.TrimPrefix(header, "Notebrew ")
	} else {
		cookie, _ := r.Cookie("authentication")
		if cookie != nil {
			rawValue = cookie.Value
		}
	}
	if rawValue == "" {
		return nil
	}
	authenticationToken, err := hex.DecodeString(fmt.Sprintf("%048s", rawValue))
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

// https://yourbasic.org/golang/formatting-byte-size-to-human-readable-format/
func fileSizeToString(size int64) string {
	if size < 0 {
		return ""
	}
	const unit = 1000
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}
	div, exp := int64(unit), 0
	for n := size / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(size)/float64(div), "kMGTPE"[exp])
}

var errorTemplate = template.Must(template.ParseFS(rootFS, "error.html"))

func forbidden(w http.ResponseWriter, r *http.Request) {
	const genericErrorMessage = "The server encountered an error. It's a bug on our end."
	logger, ok := r.Context().Value(loggerKey).(*slog.Logger)
	if !ok {
		logger = slog.Default()
	}
	buf := bufPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer bufPool.Put(buf)
	err := errorTemplate.Execute(buf, map[string]string{
		"Referer":  r.Referer(),
		"Title":    "forbidden",
		"Headline": "You do not have permission to view this page.",
	})
	if err != nil {
		logger.Error(err.Error())
		http.Error(w, genericErrorMessage, http.StatusInternalServerError)
		return
	}
	w.Header().Add("Content-Security-Policy", defaultContentSecurityPolicy)
	w.WriteHeader(http.StatusForbidden)
	buf.WriteTo(w)
}

func notFound(w http.ResponseWriter, r *http.Request) {
	const genericErrorMessage = "The server encountered an error. It's a bug on our end."
	logger, ok := r.Context().Value(loggerKey).(*slog.Logger)
	if !ok {
		logger = slog.Default()
	}
	buf := bufPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer bufPool.Put(buf)
	err := errorTemplate.Execute(buf, map[string]string{
		"Referer":  r.Referer(),
		"Title":    "404 not found",
		"Headline": "404 not found",
		"Byline":   "The page you are looking for does not exist.",
	})
	if err != nil {
		logger.Error(err.Error())
		http.Error(w, genericErrorMessage, http.StatusInternalServerError)
		return
	}
	w.Header().Add("Content-Security-Policy", defaultContentSecurityPolicy)
	w.WriteHeader(http.StatusNotFound)
	buf.WriteTo(w)
}

func internalServerError(w http.ResponseWriter, r *http.Request, serverErr error) {
	const genericErrorMessage = "The server encountered an error. It's a bug on our end."
	logger, ok := r.Context().Value(loggerKey).(*slog.Logger)
	if !ok {
		logger = slog.Default()
	}
	buf := bufPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer bufPool.Put(buf)
	var data map[string]string
	if errors.Is(serverErr, context.DeadlineExceeded) {
		data = map[string]string{
			"Referer":  r.Referer(),
			"Title":    "deadline exceeded",
			"Headline": "The server took too long to respond.",
		}
	} else {
		data = map[string]string{
			"Referer":  r.Referer(),
			"Title":    "server error",
			"Headline": "The server encountered an error.",
			"Byline":   "It's a bug on our end.",
		}
	}
	err := errorTemplate.Execute(buf, data)
	if err != nil {
		logger.Error(err.Error())
		http.Error(w, genericErrorMessage, http.StatusInternalServerError)
		return
	}
	w.Header().Add("Content-Security-Policy", defaultContentSecurityPolicy)
	w.WriteHeader(http.StatusInternalServerError)
	buf.WriteTo(w)
}

var goldmarkParser = func() parser.Parser {
	md := goldmark.New()
	md.Parser().AddOptions(parser.WithAttribute())
	extension.Table.Extend(md)
	return md.Parser()
}()

func stripMarkdownStyles(dest io.Writer, src []byte) {
	var node ast.Node
	nodes := []ast.Node{goldmarkParser.Parse(text.NewReader(src))}
	for len(nodes) > 0 {
		node, nodes = nodes[len(nodes)-1], nodes[:len(nodes)-1]
		if node == nil {
			continue
		}
		switch node := node.(type) {
		case *ast.Text:
			dest.Write(node.Text(src))
		}
		nodes = append(nodes, node.NextSibling(), node.FirstChild())
	}
}

func getTitleAndPreview(r io.ReadCloser) (title, preview string) {
	// reference:
	// https://github.com/bokwoon95/nb4/blob/68a2df18cdbeb94ff359233e7ddc54f6afe27c79/test/main.go
	defer r.Close()
	reader := bufio.NewReader(r)
	done := false
	for {
		if done {
			return title, preview
		}
		line, err := reader.ReadBytes('\n')
		if err != nil {
			done = true
		}
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		if title == "" {
			var b strings.Builder
			stripMarkdownStyles(&b, line)
			title = b.String()
			continue
		}
		if preview == "" {
			var b strings.Builder
			stripMarkdownStyles(&b, line)
			preview = b.String()
			continue
		}
		return title, preview
	}
}

func NewID() [16]byte {
	var timestamp [8]byte
	binary.BigEndian.PutUint64(timestamp[:], uint64(time.Now().Unix()))
	var id [16]byte
	const timestamp_len = 5
	const offset = len(timestamp) - timestamp_len
	copy(id[:], timestamp[offset:])
	_, err := rand.Read(id[timestamp_len:])
	if err != nil {
		panic(err)
	}
	return id
}

var base32Encoding = base32.NewEncoding("0123456789abcdefghjkmnpqrstvwxyz").WithPadding(base32.NoPadding)

func NewStringID() string {
	id := NewID()
	return base32Encoding.EncodeToString(id[:])
}
