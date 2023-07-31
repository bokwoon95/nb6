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
	var err error
	// err = automigrateCmd.Run()
	// if err != nil {
	// 	return err
	// }
	automigrateCmd.DryRun = false
	automigrateCmd.Stderr = io.Discard
	err = automigrateCmd.Run()
	if err != nil {
		return err
	}
	return nil
}

type SITE struct {
	sq.TableStruct
	SITE_NAME sq.StringField `ddl:"primarykey len=500"` // only lowercase letters, digits and hyphen
}

type USERS struct {
	sq.TableStruct
	USER_ID          sq.UUIDField   `ddl:"primarykey"`
	USERNAME         sq.StringField `ddl:"notnull len=500 unique references={site.site_name onupdate=cascade}"`
	EMAIL            sq.StringField `ddl:"notnull len=500 unique"`
	PASSWORD_HASH    sq.StringField `ddl:"notnull len=500"`
	RESET_TOKEN_HASH sq.BinaryField `ddl:"mysql:type=BINARY(40) unique"`
}

type SITE_USER struct {
	sq.TableStruct `ddl:"primarykey=site_name,user_id"`
	SITE_NAME      sq.StringField `ddl:"references={site onupdate=cascade}"`
	USER_ID        sq.UUIDField   `ddl:"references={users onupdate=cascade index}"`
}

type AUTHENTICATION struct {
	sq.TableStruct
	AUTHENTICATION_TOKEN_HASH sq.BinaryField `ddl:"mysql:type=BINARY(40) primarykey"`
	USER_ID                   sq.UUIDField   `ddl:"notnull references={users onupdate=cascade index}"`
}

type SESSION struct {
	sq.TableStruct
	SESSION_TOKEN_HASH sq.BinaryField `ddl:"mysql:type=BINARY(40) primarykey"`
	DATA               sq.JSONField
}
