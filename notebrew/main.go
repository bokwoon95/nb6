package main

import (
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
)

var open = func(address string) {}

func main() {
	// notebrew -dir -addr -multisite subdirectory
	var dir, addr, db string
	flagset := flag.NewFlagSet("", flag.ContinueOnError)
	flagset.StringVar(&dir, "dir", "", "")
	flagset.StringVar(&addr, "addr", "", "")
	flagset.StringVar(&db, "db", "", "")
	err := flagset.Parse(os.Args[1:])
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		exit(err)
	}
	dir = strings.TrimSpace(dir)
	if dir == "" {
		userHomeDir, err := os.UserHomeDir()
		if err != nil {
			exit(err)
		}
		dir = filepath.Join(userHomeDir, "notebrew_admin")
	}
	err = os.MkdirAll(dir, 0755)
	if err != nil {
		exit(err)
	}
	addr = strings.TrimSpace(addr)
	if addr != "" {
		err = os.WriteFile(filepath.Join(dir, "address.txt"), []byte(addr), 0644)
		if err != nil {
			exit(err)
		}
	}
	db = strings.TrimSpace(db)
	if db != "" {
		err = os.WriteFile(filepath.Join(dir, "database.txt"), []byte(db), 0644)
		if err != nil {
			exit(err)
		}
	}
}
