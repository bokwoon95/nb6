package nb6

import (
	"database/sql"
	"embed"
	"io"
	"os"

	"github.com/bokwoon95/sq"
	"github.com/bokwoon95/sqddl/ddl"
)

//go:embed schema.go
var schemaFS embed.FS

func automigrate(dialect string, db *sql.DB) error {
	if db == nil {
		return nil
	}
	automigrateCmd := &ddl.AutomigrateCmd{
		DB:             db,
		Dialect:        dialect,
		DirFS:          schemaFS,
		Filenames:      []string{"schema.go"},
		DropObjects:    true,
		AcceptWarnings: true,
		DryRun:         true,
		Stdout:         os.Stderr,
	}
	err := automigrateCmd.Run()
	if err != nil {
		return err
	}
	automigrateCmd.DryRun = false
	automigrateCmd.Stderr = io.Discard
	err = automigrateCmd.Run()
	if err != nil {
		return err
	}
	return nil
}

type SITES struct {
	sq.TableStruct
	SITE_ID   sq.UUIDField   `ddl:"primarykey"`
	SITE_NAME sq.StringField `ddl:"notnull len=500 unique"` // only lowercase letters, digits and hyphen
	USER_ID   sq.UUIDField   `ddl:"notnull references={users onupdate=cascade index}"`
}

type USERS struct {
	sq.TableStruct
	USER_ID          sq.UUIDField   `ddl:"primarykey"`
	SITE_ID          sq.UUIDField   `ddl:"notnull references={sites onupdate=cascade index}"`
	EMAIL            sq.StringField `ddl:"notnull len=500 unique"`
	PASSWORD_HASH    sq.StringField `ddl:"notnull len=500"`
	RESET_TOKEN_HASH sq.BinaryField `ddl:"mysql:type=BINARY(40) unique"`
}

type AUTHENTICATIONS struct {
	sq.TableStruct
	AUTHENTICATION_TOKEN_HASH sq.BinaryField `ddl:"mysql:type=BINARY(40) primarykey"`
	USER_ID                   sq.UUIDField   `ddl:"notnull references={users onupdate=cascade index}"`
}

type SESSIONS struct {
	sq.TableStruct
	SESSION_TOKEN_HASH sq.BinaryField `ddl:"mysql:type=BINARY(40) primarykey"`
	DATA               sq.JSONField
}
