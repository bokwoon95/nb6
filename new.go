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
	"golang.org/x/exp/slog"
)

func New(fsys FS) (*Notebrew, error) {
	nbrew := &Notebrew{
		FS:        fsys,
		ErrorCode: func(error) string { return "" },
	}
	localDir, err := filepath.Abs(fmt.Sprint(nbrew.FS))
	if err == nil {
		fileInfo, err := os.Stat(localDir)
		if err != nil || !fileInfo.IsDir() {
			localDir = ""
		}
	}

	// Read from address.txt.
	b, err := fs.ReadFile(nbrew.FS, "address.txt")
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%s: %v", filepath.Join(localDir, "address.txt"), err)
		}
		nbrew.Protocol = "http://"
		nbrew.AdminDomain = "localhost:6444"
		nbrew.ContentDomain = "localhost:6444"
	} else {
		address := strings.TrimSpace(string(b))
		if address == "" {
			nbrew.Protocol = "http://"
			nbrew.AdminDomain = "localhost:6444"
			nbrew.ContentDomain = "localhost:6444"
		} else if strings.HasPrefix(address, ":") {
			_, err = strconv.Atoi(address[1:])
			if err != nil {
				return nil, fmt.Errorf(
					"%s: %q is not a valid port (must be a number e.g. :6444)",
					filepath.Join(localDir, "address.txt"),
					address,
				)
			}
			nbrew.Protocol = "http://"
			nbrew.AdminDomain = "localhost" + address
			nbrew.ContentDomain = "localhost" + address
		} else {
			nbrew.Protocol = "https://"
			lines := strings.Split(address, "\n")
			if len(lines) == 1 {
				nbrew.AdminDomain = strings.TrimSpace(lines[0])
				nbrew.ContentDomain = strings.TrimSpace(lines[0])
			} else if len(lines) == 2 {
				nbrew.AdminDomain = strings.TrimSpace(lines[0])
				nbrew.ContentDomain = strings.TrimSpace(lines[1])
			} else {
				return nil, fmt.Errorf("%s contains too many lines, maximum 2 lines." +
					" The first line is the admin domain, the second line is the content domain." +
					" Alternatively, if only one line is provided it will be used as as both the admin domain and content domain." +
					filepath.Join(localDir, "address.txt"),
				)
			}
			if !strings.Contains(nbrew.AdminDomain, ".") {
				return nil, fmt.Errorf("%s: %q is not a valid domain (e.g. example.com):"+
					" missing a top level domain (.com, .org, .net, etc)",
					filepath.Join(localDir, "address.txt"),
					nbrew.AdminDomain,
				)
			}
			for _, char := range nbrew.AdminDomain {
				if (char >= '0' && char <= '9') || (char >= 'a' && char <= 'z') || char == '.' || char == '-' {
					continue
				}
				return nil, fmt.Errorf("%s: %q is not a valid domain (e.g. example.com):"+
					" only lowercase letters, numbers, dot and hyphen are allowed",
					filepath.Join(localDir, "address.txt"),
					nbrew.AdminDomain,
				)
			}
			if !strings.Contains(nbrew.ContentDomain, ".") {
				return nil, fmt.Errorf("%s: %q is not a valid domain (e.g. example.com):"+
					" missing a top level domain (.com, .org, .net, etc)",
					filepath.Join(localDir, "address.txt"),
					nbrew.ContentDomain,
				)
			}
			for _, char := range nbrew.ContentDomain {
				if (char >= '0' && char <= '9') || (char >= 'a' && char <= 'z') || char == '.' || char == '-' {
					continue
				}
				return nil, fmt.Errorf("%s: %q is not a valid domain (e.g. example.com):"+
					" only lowercase letters, numbers, dot and hyphen are allowed",
					filepath.Join(localDir, "address.txt"),
					nbrew.ContentDomain,
				)
			}
		}
	}

	// Read from multisite.txt.
	b, err = fs.ReadFile(nbrew.FS, "multisite.txt")
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%s: %v", filepath.Join(localDir, "multisite.txt"), err)
		}
	} else {
		nbrew.MultisiteMode = strings.ToLower(string(b))
	}
	if nbrew.MultisiteMode != "" && nbrew.MultisiteMode != "subdomain" && nbrew.MultisiteMode != "subdirectory" {
		return nil, fmt.Errorf(
			`%s: %q is not a valid multisite value (accepted values: "", "subdomain", "subdirectory")`,
			filepath.Join(localDir, "multisite.txt"),
			nbrew.MultisiteMode,
		)
	}

	// Read from database.txt.
	var dsn string
	b, err = fs.ReadFile(nbrew.FS, "database.txt")
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		if nbrew.Protocol == "https://" {
			// If database.txt doesn't exist but we are serving a live site, we
			// have to create a database. In this case, fall back to an SQLite
			// database.
			dsn = "sqlite"
		}
	} else {
		dsn = strings.TrimSpace(string(b))
		if strings.HasPrefix(dsn, "file:") {
			filename := strings.TrimPrefix(strings.TrimPrefix(dsn, "file:"), "//")
			file, err := os.Open(filename)
			if err != nil {
				ext := filepath.Ext(filename)
				if errors.Is(err, fs.ErrNotExist) && (ext == ".sqlite" || ext == ".sqlite3" || ext == ".db" || ext == ".db3") {
					dsn = filename
				} else {
					return nil, fmt.Errorf("%s: opening %q: %v", filepath.Join(localDir, "database.txt"), dsn, err)
				}
			} else {
				defer file.Close()
				r := bufio.NewReader(file)
				// SQLite databases may also start with a 'file:' prefix. Treat
				// the contents of the file as a dsn only if the file isn't
				// already an SQLite database i.e. the first 16 bytes isn't the
				// SQLite file header.
				// https://www.sqlite.org/fileformat.html#the_database_header
				header, err := r.Peek(16)
				if err != nil {
					return nil, fmt.Errorf("%s: reading %q: %v", filepath.Join(localDir, "database.txt"), dsn, err)
				}
				if string(header) == "SQLite format 3\x00" {
					dsn = "sqlite:" + filename
				} else {
					var b strings.Builder
					_, err = r.WriteTo(&b)
					if err != nil {
						return nil, fmt.Errorf("%s: reading %q: %v", filepath.Join(localDir, "database.txt"), dsn, err)
					}
					dsn = strings.TrimSpace(b.String())
				}
			}
		}
	}
	if dsn != "" {
		// Determine the database dialect from the dsn.
		if dsn == "sqlite" {
			nbrew.Dialect = "sqlite"
			if localDir == "" {
				return nil, fmt.Errorf("unable to create sqlite database")
			}
			dsn = filepath.Join(localDir, "notebrew.db")
		} else if strings.HasPrefix(dsn, "sqlite:") || strings.HasPrefix(dsn, "sqlite3:") {
			nbrew.Dialect = "sqlite"
		} else if strings.HasPrefix(dsn, "postgres://") {
			nbrew.Dialect = "postgres"
		} else if strings.HasPrefix(dsn, "mysql://") {
			nbrew.Dialect = "mysql"
		} else if strings.HasPrefix(dsn, "sqlserver://") {
			nbrew.Dialect = "sqlserver"
		} else if strings.Contains(dsn, "@tcp(") || strings.Contains(dsn, "@unix(") {
			nbrew.Dialect = "mysql"
		} else {
			ext := filepath.Ext(dsn)
			if ext == ".sqlite" || ext == ".sqlite3" || ext == ".db" || ext == ".db3" {
				nbrew.Dialect = "sqlite"
			} else {
				return nil, fmt.Errorf("%s: unknown or unsupported dataSourceName %q", filepath.Join(localDir, "database.txt"), dsn)
			}
		}
		// Set a default driverName depending on the dialect.
		var driverName string
		switch nbrew.Dialect {
		case "sqlite":
			// Assumes driver will be github.com/mattn/go-sqlite3.
			driverName = "sqlite3"
		case "postgres":
			// Assumes driver will be github.com/lib/pq.
			driverName = "postgres"
		case "mysql":
			// Assumes driver will be github.com/go-sql-driver/mysql.
			driverName = "mysql"
		case "sqlserver":
			// Assumes driver will be github.com/denisenkom/go-mssqldb.
			driverName = "sqlserver"
		}
		// Check if the user registered any driverName/dsn overrides
		// for the dialect.
		dbDriversMu.RLock()
		d := dbDrivers[nbrew.Dialect]
		dbDriversMu.RUnlock()
		if d.DriverName != "" {
			driverName = d.DriverName
		}
		if d.PreprocessDSN != nil {
			dsn, err = d.PreprocessDSN(dsn)
			if err != nil {
				return nil, err
			}
		} else {
			// Do some default dsn cleaning if no custom dialect Driver was
			// registered. We assume the default drivers of
			// "github.com/mattn/go-sqlite3" and
			// "github.com/go-sql-driver/mysql", which don't accept "sqlite:"
			// or "mysql://" prefixes so trim that away.
			switch nbrew.Dialect {
			case "sqlite":
				if strings.HasPrefix(dsn, "sqlite3:") {
					dsn = strings.TrimPrefix(strings.TrimPrefix(dsn, "sqlite3:"), "//")
				} else if strings.HasPrefix(dsn, "sqlite:") {
					dsn = strings.TrimPrefix(strings.TrimPrefix(dsn, "sqlite:"), "//")
				}
			case "mysql":
				dsn = strings.TrimPrefix(dsn, "mysql://")
			}
		}
		if d.ErrorCode != nil {
			nbrew.ErrorCode = d.ErrorCode
		}
		// Open the database using the driverName and dsn.
		nbrew.DB, err = sql.Open(driverName, dsn)
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
