package nb6

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/caddyserver/certmagic"
	"golang.org/x/exp/slices"
	"golang.org/x/exp/slog"
)

func New(fsys FS) (*Notebrew, error) {
	nbrew := &Notebrew{
		FS:        fsys,
		ErrorCode: func(error) string { return "" },
	}
	adminDir, err := filepath.Abs(fmt.Sprint(nbrew.FS))
	if err == nil {
		fileInfo, err := os.Stat(adminDir)
		if err != nil || !fileInfo.IsDir() {
			adminDir = ""
		}
	}
	nbrew.Protocol, nbrew.AdminDomain, nbrew.ContentDomain, err = getDomains(nbrew.FS, adminDir)
	if err != nil {
		return nil, err
	}
	nbrew.MultisiteMode, err = getMultisiteMode(nbrew.FS, adminDir)
	if err != nil {
		return nil, err
	}

	// Read from database.txt.
	var driverName string
	var dataSourceName string
	nbrew.Dialect, driverName, dataSourceName, err = getDataSourceName(nbrew.FS, adminDir, nbrew.Protocol)
	if err != nil {
		return nil, err
	}
	b, err = fs.ReadFile(nbrew.FS, "database.txt")
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		if nbrew.Protocol == "https://" {
			// If database.txt doesn't exist but we are serving a live site, we
			// have to create a database. In this case, fall back to an SQLite
			// database.
			nbrew.Dialect = "sqlite"
			driverName = "sqlite3"
			dataSourceName = filepath.Join(adminDir, "notebrew.db")
		}
	} else {
		dataSourceName = strings.TrimSpace(string(b))
		if strings.HasPrefix(dataSourceName, "file:") {
			filename, _, _ := strings.Cut(strings.TrimPrefix(strings.TrimPrefix(dataSourceName, "file:"), "//"), "?")
			file, err := os.Open(filename)
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) && slices.Contains([]string{".sqlite", ".sqlite3", ".db", ".db3"}, filepath.Ext(filename)) {
					nbrew.Dialect = "sqlite"
					driverName = "sqlite3"
					dataSourceName = filepath.Join(adminDir, "notebrew.db")
				} else {
					return nil, fmt.Errorf("%s: opening %q: %v", filepath.Join(adminDir, "database.txt"), dataSourceName, err)
				}
			} else {
				defer file.Close()
				r := bufio.NewReader(file)
				// SQLite databases may also start with a 'file:' prefix. Treat the
				// contents of the file as a dsn only if the file isn't already an
				// SQLite database i.e. the first 16 bytes isn't the SQLite file
				// header. https://www.sqlite.org/fileformat.html#the_database_header
				header, err := r.Peek(16)
				if err != nil {
					return nil, fmt.Errorf("%s: reading %q: %v", filepath.Join(adminDir, "database.txt"), dataSourceName, err)
				}
				if string(header) == "SQLite format 3\x00" {
					dataSourceName = "sqlite:" + dataSourceName
				} else {
					var b strings.Builder
					_, err = r.WriteTo(&b)
					if err != nil {
						return "", "", ""
					}
					dataSourceName = strings.TrimSpace(b.String())
				}
			}
		}
		// Determine the database dialect from the dsn.
		if dataSourceName == "sqlite" {
			nbrew.Dialect = "sqlite"
		} else if strings.HasPrefix(dataSourceName, "postgres://") {
			nbrew.Dialect = "postgres"
		} else if strings.HasPrefix(dataSourceName, "mysql://") {
			nbrew.Dialect = "mysql"
		} else if strings.HasPrefix(dataSourceName, "sqlserver://") {
			nbrew.Dialect = "sqlserver"
		} else if strings.Contains(dataSourceName, "@tcp(") || strings.Contains(dataSourceName, "@unix(") {
			nbrew.Dialect = "mysql"
		} else if dataSourceName != "" {
			return nil, fmt.Errorf("database.txt: unknown dsn %q", dataSourceName)
		}
	}
	if nbrew.Dialect == "sqlserver" {
		// NOTE: not supporting sqlserver until I can insert NULL (VAR)BINARY
		// values into the database
		// (https://github.com/denisenkom/go-mssqldb/issues/196).
		return nil, fmt.Errorf("database.txt: sqlserver is not supported")
	}
	if nbrew.Dialect == "sqlite" {
		// SQLite databases can only be created by giving it a filepath on the
		// current system. Check if we can convert srv.fs into a filepath
		// string, then check if it is a valid directory. If it is, we can
		// create an SQLite database there.
		if adminDir == "" {
			return nil, fmt.Errorf("unable to create DB")
		}
	}
	if dataSourceName != "" {
		var driverName string
		// Set a default driverName depending on the dialect.
		switch nbrew.Dialect {
		case "sqlite":
			driverName = "sqlite3"
		case "postgres":
			driverName = "postgres"
		case "mysql":
			driverName = "mysql"
		case "sqlserver":
			driverName = "sqlserver"
		}
		// Check if the user registered any driverName/dsn overrides for the
		// dialect.
		dbDriversMu.RLock()
		d := dbDrivers[nbrew.Dialect]
		dbDriversMu.RUnlock()
		if d.DriverName != "" {
			driverName = d.DriverName
		}
		if d.PreprocessDSN != nil {
			dataSourceName, err = d.PreprocessDSN(dataSourceName)
			if err != nil {
				return nil, err
			}
		} else {
			if nbrew.Dialect == "sqlite" {
				if strings.HasPrefix(dataSourceName, "sqlite3:") {
					dataSourceName = strings.TrimPrefix(strings.TrimPrefix(dataSourceName, "sqlite3:"), "//")
				} else if strings.HasPrefix(dataSourceName, "sqlite:") {
					dataSourceName = strings.TrimPrefix(strings.TrimPrefix(dataSourceName, "sqlite:"), "//")
				}
			} else if nbrew.Dialect == "mysql" {
				dataSourceName = strings.TrimPrefix(dataSourceName, "mysql://")
			}
		}
		if d.ErrorCode != nil {
			nbrew.ErrorCode = d.ErrorCode
		}
		// Open the database using the driverName and dsn.
		nbrew.DB, err = sql.Open(driverName, dataSourceName)
		if err != nil {
			return nil, err
		}
		err = automigrate(nbrew.Dialect, nbrew.DB)
		if err != nil {
			return nil, err
		}
	}

	dirs := []string{
		"posts",
		"notes",
		"pages",
		"templates",
		"assets",
		"images",
		"system",
	}
	for _, dir := range dirs {
		err = nbrew.FS.Mkdir(dir, 0755)
		if err != nil && !errors.Is(err, fs.ErrExist) {
			log.Println(err)
		}
	}
	return nbrew, nil
}

func (nbrew *Notebrew) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Clean the path and redirect if necessary.
	if r.Method == "GET" {
		cleanedPath := path.Clean(r.URL.Path)
		if cleanedPath != "/" && path.Ext(cleanedPath) == "" {
			cleanedPath += "/"
		}
		if cleanedPath != r.URL.Path {
			cleanedURL := *r.URL
			cleanedURL.Path = cleanedPath
			http.Redirect(w, r, cleanedURL.String(), http.StatusMovedPermanently)
			return
		}
	}
	// Inject the request method and url into the logger.
	logger, ok := r.Context().Value(loggerKey).(*slog.Logger)
	if !ok {
		logger = slog.Default()
	}
	r = r.WithContext(context.WithValue(r.Context(), loggerKey, logger.With(
		slog.String("method", r.Method),
		slog.String("url", r.URL.String()),
	)))
	segment, _, _ := strings.Cut(strings.Trim(r.URL.Path, "/"), "/")
	if r.Host == nbrew.AdminDomain && segment == "admin" {
		nbrew.admin(w, r)
		return
	}
	nbrew.content(w, r)
}

func (nbrew *Notebrew) NewServer() (*http.Server, error) {
	server := &http.Server{
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
		IdleTimeout:  120 * time.Second,
		Addr:         nbrew.AdminDomain,
		ErrorLog:     log.New(io.Discard, "", 0),
		Handler:      nbrew,
	}
	if nbrew.Protocol == "https://" {
		server.Addr = ":443"
		certConfig := certmagic.NewDefault()
		domainNames := []string{nbrew.AdminDomain}
		if nbrew.ContentDomain != "" && nbrew.ContentDomain != nbrew.AdminDomain {
			domainNames = append(domainNames, nbrew.ContentDomain)
		}
		if nbrew.MultisiteMode == "subdomain" {
			if certmagic.DefaultACME.DNS01Solver == nil && certmagic.DefaultACME.CA == certmagic.LetsEncryptProductionCA {
				return nil, fmt.Errorf("DNS-01 solver not configured, cannot use subdomains")
			}
			domainNames = append(domainNames, "*."+nbrew.ContentDomain)
		}
		err := certConfig.ManageAsync(context.Background(), domainNames)
		if err != nil {
			return nil, err
		}
		server.TLSConfig = certConfig.TLSConfig()
		server.TLSConfig.NextProtos = []string{"h2", "http/1.1", "acme-tls/1"}
	}
	return server, nil
}

func getDomains(fsys fs.FS, fsysName string) (scheme, adminDomain, contentDomain string, err error) {
	b, err := fs.ReadFile(fsys, "address.txt")
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "http://", "localhost:6444", "localhost:6444", nil
		}
		return "", "", "", fmt.Errorf("%s: %v", filepath.Join(fsysName, "address.txt"), err)
	}
	address := strings.TrimSpace(string(b))
	if address == "" {
		return "http://", "localhost:6444", "localhost:6444", nil
	}
	if strings.HasPrefix(address, ":") {
		_, err = strconv.Atoi(address[1:])
		if err != nil {
			return "", "", "", fmt.Errorf(
				"%s: %q is not a valid port (must be a number e.g. :6444)",
				filepath.Join(fsysName, "address.txt"),
				address,
			)
		}
		return "http://", "localhost:6444", "localhost:6444", nil
	}
	lines := strings.Split(address, "\n")
	if len(lines) == 1 {
		adminDomain = strings.TrimSpace(lines[0])
		contentDomain = strings.TrimSpace(lines[0])
	} else if len(lines) == 2 {
		adminDomain = strings.TrimSpace(lines[0])
		contentDomain = strings.TrimSpace(lines[1])
	} else {
		return "", "", "", fmt.Errorf("%s contains too many lines, maximum 2 lines." +
			" The first line is the admin domain, the second line is the content domain." +
			" Alternatively, if only one line is provided it will be used as as both the admin domain and content domain." +
			filepath.Join(fsysName, "address.txt"),
		)
	}
	if !strings.Contains(adminDomain, ".") {
		return "", "", "", fmt.Errorf("%s: %q is not a valid domain (e.g. example.com):"+
			" missing a top level domain (.com, .org, .net, etc)",
			filepath.Join(fsysName, "address.txt"),
			adminDomain,
		)
	}
	for _, char := range adminDomain {
		if (char >= '0' && char <= '9') || (char >= 'a' && char <= 'z') || char == '.' || char == '-' {
			continue
		}
		return "", "", "", fmt.Errorf("%s: %q is not a valid domain (e.g. example.com):"+
			" only lowercase letters, numbers, dot and hyphen are allowed",
			filepath.Join(fsysName, "address.txt"),
			adminDomain,
		)
	}
	if !strings.Contains(contentDomain, ".") {
		return "", "", "", fmt.Errorf("%s: %q is not a valid domain (e.g. example.com):"+
			" missing a top level domain (.com, .org, .net, etc)",
			filepath.Join(fsysName, "address.txt"),
			contentDomain,
		)
	}
	for _, char := range contentDomain {
		if (char >= '0' && char <= '9') || (char >= 'a' && char <= 'z') || char == '.' || char == '-' {
			continue
		}
		return "", "", "", fmt.Errorf("%s: %q is not a valid domain (e.g. example.com):"+
			" only lowercase letters, numbers, dot and hyphen are allowed",
			filepath.Join(fsysName, "address.txt"),
			contentDomain,
		)
	}
	return "https://", adminDomain, contentDomain, nil
}

// TODO: if fsysName is purely for reporting purposes, we can defer the
// embedding of the full file path to the caller instead.
func getMultisiteMode(fsys fs.FS, fsysName string) (multisiteMode string, err error) {
	b, err := fs.ReadFile(fsys, "multisite.txt")
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil
		}
		return "", fmt.Errorf("%s: %v", filepath.Join(fsysName, "multisite.txt"), err)
	}
	multisiteMode = strings.ToLower(string(b))
	if multisiteMode != "" && multisiteMode != "subdomain" && multisiteMode != "subdirectory" {
		return "", fmt.Errorf(
			`%s: %q is not a valid multisite value (accepted values: "", "subdomain", "subdirectory")`,
			filepath.Join(fsysName, "multisite.txt"),
			multisiteMode,
		)
	}
	return multisiteMode, nil
}

// TODO: FUHG I still need to get the ErrorCode from the driver. Revamp this function's signature to accept a *Notebrew perhaps?
func getDataSourceName(fsys fs.FS, fsysName string, protocol string) (dialect string, driverName string, dataSourceName string, err error) {
	b, err := fs.ReadFile(fsys, "database.txt")
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			if protocol == "https://" {
				return "sqlite", "sqlite3", filepath.Join(fsysName, "notebrew.db"), nil
			}
			return "", "", "", nil
		}
		return "", "", "", fmt.Errorf("%s: %v", filepath.Join(fsysName, "database.txt"), err)
	}
	dataSourceName = strings.TrimSpace(string(b))
	if strings.HasPrefix(dataSourceName, "file:") {
		filename, _, _ := strings.Cut(strings.TrimPrefix(strings.TrimPrefix(dataSourceName, "file:"), "//"), "?")
		file, err := os.Open(filename)
		if err != nil {
			ext := filepath.Ext(filename)
			if errors.Is(err, fs.ErrNotExist) && (ext == ".sqlite" || ext == ".sqlite3" || ext == ".db" || ext == ".db3") {
				return "sqlite", "sqlite3", filename, nil
			}
			return "", "", "", fmt.Errorf("%s: opening %q: %v", filepath.Join(fsysName, "database.txt"), dataSourceName, err)
		} else {
			defer file.Close()
			r := bufio.NewReader(file)
			// SQLite databases may also start with a 'file:' prefix. Treat the
			// contents of the file as a dsn only if the file isn't already an
			// SQLite database i.e. the first 16 bytes isn't the SQLite file
			// header. https://www.sqlite.org/fileformat.html#the_database_header
			header, err := r.Peek(16)
			if err != nil {
				return "", "", "", fmt.Errorf("%s: reading %q: %v", filepath.Join(fsysName, "database.txt"), dataSourceName, err)
			}
			if string(header) == "SQLite format 3\x00" {
				dataSourceName = "sqlite:" + dataSourceName
			} else {
				var b strings.Builder
				_, err = r.WriteTo(&b)
				if err != nil {
					return "", "", "", fmt.Errorf("%s: reading %q: %v", filepath.Join(fsysName, "database.txt"), dataSourceName, err)
				}
				dataSourceName = strings.TrimSpace(b.String())
			}
		}
	}
	if dataSourceName == "sqlite" {
		dialect = "sqlite"
		dataSourceName = filepath.Join(fsysName, "notebrew.db")
	} else if strings.HasPrefix(dataSourceName, "postgres://") {
		dialect = "postgres"
	} else if strings.HasPrefix(dataSourceName, "mysql://") {
		dialect = "mysql"
	} else {
		dsn, _, _ := strings.Cut(dataSourceName, "?")
		ext := filepath.Ext(dsn)
		if ext == ".sqlite" || ext == ".sqlite3" || ext == ".db" || ext == ".db3" {
			dialect = "sqlite"
		} else {
			return "", "", "", fmt.Errorf("%s: unknown or unsupported dataSourceName %q", filepath.Join(fsysName, "database.txt"), dataSourceName)
		}
	}
	dbDriversMu.RLock()
	driver, ok := dbDrivers[dialect]
	dbDriversMu.RUnlock()
	if ok {
		driverName = driver.DriverName
		if driver.PreprocessDSN != nil {
			newDataSourceName, err := driver.PreprocessDSN(dataSourceName)
			if err != nil {
				return "", "", "", fmt.Errorf("%s: error occurred when trying to preprocess %q: %v", filepath.Join(fsysName, "database.txt"), dataSourceName, err)
			}
			return dialect, driver.DriverName, newDataSourceName, nil
		}
		return dialect, driver.DriverName, dataSourceName, nil
	}
	switch dialect {
	case "sqlite":
		var tmp string
		if strings.HasPrefix(dataSourceName, "sqlite3:") {
			tmp = strings.TrimPrefix(dataSourceName, "sqlite3:")
		} else {
			tmp = strings.TrimPrefix(dataSourceName, "sqlite:")
		}
		return dialect, "sqlite3", strings.TrimPrefix(tmp, "//"), nil
	case "postgres":
		return dialect, "postgres", dataSourceName, nil
	case "mysql":
		return dialect, "mysql", strings.TrimPrefix(dataSourceName, "mysql://"), nil
	}
	return "", "", "", fmt.Errorf("%s: unknown or unsupported dataSourceName %q", filepath.Join(fsysName, "database.txt"), dataSourceName)
}

var (
	dbDriversMu sync.RWMutex
	dbDrivers   = make(map[string]Driver)
)

// Driver represents the capabilities of the underlying database driver for a
// particular dialect. It is not necessary to implement all fields.
type Driver struct {
	// (Required) Dialect is the database dialect. Possible values: "sqlite", "postgres",
	// "mysql".
	Dialect string

	// (Required) DriverName is the driverName to be used with sql.Open().
	DriverName string

	// ErrorCode translates a database error into an dialect-specific error
	// code. If the error is not a database error or no error code can be
	// determined, ErrorCode should return an empty string.
	ErrorCode func(error) string

	// If not nil, PreprocessDSN will be called on a dataSourceName right
	// before it is passed in to sql.Open().
	PreprocessDSN func(string) (string, error)
}

// Registers registers a driver for a particular database dialect.
func RegisterDriver(d Driver) {
	dbDriversMu.Lock()
	defer dbDriversMu.Unlock()
	if d.Dialect == "" {
		panic("notebrew: driver dialect cannot be empty")
	}
	if _, dup := dbDrivers[d.Dialect]; dup {
		panic("notebrew: RegisterDialect called twice for dialect " + d.Dialect)
	}
	dbDrivers[d.Dialect] = d
}
