package nb6

import (
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

func New(fsys FS) (*Notebrew, error) {
	nbrew := &Notebrew{
		FS:        fsys,
		ErrorCode: func(error) string { return "" },
	}

	// Read from address.txt.
	var address string
	b, err := fs.ReadFile(nbrew.FS, "address.txt")
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		address = ":6444"
	} else {
		address = strings.TrimSpace(string(b))
	}
	if strings.HasPrefix(address, ":") {
		// If address starts with ":", it's a localhost port.
		nbrew.Scheme = "http://"
		_, err = strconv.Atoi(address[1:])
		if err != nil {
			return nil, fmt.Errorf("address.txt: %q is not a valid port", address)
		}
		nbrew.AdminDomain = "localhost" + address
		nbrew.ContentDomain = "localhost" + address
	} else {
		// Make sure address is not empty.
		if address == "" {
			return nil, fmt.Errorf("address.txt: address cannot be empty")
		}
		nbrew.Scheme = "https://"
		nbrew.AdminDomain, nbrew.ContentDomain, _ = strings.Cut(address, "\n")
		nbrew.ContentDomain = strings.TrimSpace(nbrew.ContentDomain)
		if strings.Contains(nbrew.ContentDomain, "\n") {
			return nil, fmt.Errorf("address.txt: too many lines, maximum 2")
		}
		if nbrew.ContentDomain == "" {
			nbrew.ContentDomain = nbrew.AdminDomain
		}
		// Validate that domain only contains characters [a-zA-Z0-9.-].
		for _, char := range nbrew.AdminDomain {
			if (char >= '0' && char <= '9') || (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || char == '.' || char == '-' {
				continue
			}
			return nil, fmt.Errorf("address.txt: invalid domain name %q: only alphabets, numbers, dot and hyphen are allowed", address)
		}
		for _, char := range nbrew.ContentDomain {
			if (char >= '0' && char <= '9') || (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || char == '.' || char == '-' {
				continue
			}
			return nil, fmt.Errorf("address.txt: invalid domain name %q: only alphabets, numbers, dot and hyphen are allowed", address)
		}
	}

	// Read from multisite.txt.
	b, err = fs.ReadFile(nbrew.FS, "multisite.txt")
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		nbrew.MultisiteMode = ""
	} else {
		nbrew.MultisiteMode = strings.ToLower(string(b))
		if nbrew.MultisiteMode != "" && nbrew.MultisiteMode != "subdomain" && nbrew.MultisiteMode != "subdirectory" {
			return nil, fmt.Errorf("invalid multisite mode %q", string(b))
		}
	}

	// Read from database.txt.
	var dsn string
	b, err = fs.ReadFile(nbrew.FS, "database.txt")
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		if nbrew.AdminDomain != "" {
			// If database.txt doesn't exist but we are serving a live site, we
			// have to create a database. In this case, fall back to an SQLite
			// database.
			nbrew.Dialect = "sqlite"
		}
	} else {
		dsn = strings.TrimSpace(string(b))
		// Determine the database dialect from the dsn.
		if dsn == "sqlite" {
			nbrew.Dialect = "sqlite"
		} else if strings.HasPrefix(dsn, "postgres://") {
			nbrew.Dialect = "postgres"
		} else if strings.HasPrefix(dsn, "mysql://") {
			nbrew.Dialect = "mysql"
		} else if strings.HasPrefix(dsn, "sqlserver://") {
			nbrew.Dialect = "sqlserver"
		} else if strings.Contains(dsn, "@tcp(") || strings.Contains(dsn, "@unix(") {
			nbrew.Dialect = "mysql"
		} else if dsn != "" {
			return nil, fmt.Errorf("database.txt: unknown dsn %q", dsn)
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
		dir, err := filepath.Abs(fmt.Sprint(nbrew.FS))
		if err != nil {
			return nil, fmt.Errorf("unable to create DB")
		}
		fileinfo, err := os.Stat(dir)
		if err != nil {
			return nil, fmt.Errorf("unable to create DB")
		}
		if !fileinfo.IsDir() {
			return nil, fmt.Errorf("unable to create DB")
		}
		dsn = filepath.Join(dir, "notebrew.db")
	}
	if dsn != "" {
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
			dsn, err = d.PreprocessDSN(dsn)
			if err != nil {
				return nil, err
			}
		} else {
			if nbrew.Dialect == "sqlite" {
				if strings.HasPrefix(dsn, "sqlite3:") {
					dsn = strings.TrimPrefix(strings.TrimPrefix(dsn, "sqlite3:"), "//")
				} else if strings.HasPrefix(dsn, "sqlite:") {
					dsn = strings.TrimPrefix(strings.TrimPrefix(dsn, "sqlite:"), "//")
				}
			} else if nbrew.Dialect == "mysql" {
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

	return nbrew, nil
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
