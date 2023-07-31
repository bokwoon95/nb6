package main

import (
	"bufio"
	"crypto/subtle"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/bokwoon95/nb6"
	"github.com/bokwoon95/sq"
	"github.com/oklog/ulid/v2"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/term"
)

type CreateUserCmd struct {
	Notebrew     *nb6.Notebrew
	Stderr       io.Writer
	Username     string
	Email        string
	PasswordHash string
}

func CreateUserCommand(nb *nb6.Notebrew, args ...string) (*CreateUserCmd, error) {
	var cmd CreateUserCmd
	cmd.Notebrew = nb
	flagset := flag.NewFlagSet("", flag.ContinueOnError)
	flagset.StringVar(&cmd.Username, "username", "", "")
	flagset.StringVar(&cmd.Email, "email", "", "")
	flagset.StringVar(&cmd.PasswordHash, "password-hash", "", "")
	err := flagset.Parse(args)
	if err != nil {
		return nil, err
	}
	flagArgs := flagset.Args()
	if len(flagArgs) > 0 {
		flagset.Usage()
		return nil, fmt.Errorf("unexpected arguments: %s", strings.Join(flagArgs, " "))
	}
	reader := bufio.NewReader(os.Stdin)

	cmd.Username = strings.TrimSpace(cmd.Username)
	if cmd.Username == "" {
		for {
			fmt.Print("Username (leave blank for the default user): ")
			text, err := reader.ReadString('\n')
			if err != nil {
				return nil, err
			}
			cmd.Username = strings.TrimSpace(text)
			exists, err := sq.FetchExists(cmd.Notebrew.DB, sq.CustomQuery{
				Dialect: cmd.Notebrew.Dialect,
				Format:  "SELECT 1 FROM site WHERE site_name = {username}",
				Values: []any{
					sq.StringParam("username", cmd.Username),
				},
			})
			if err != nil {
				return nil, err
			}
			if exists {
				fmt.Println("Username already taken.")
				continue
			}
			break
		}
	}

	cmd.Email = strings.TrimSpace(cmd.Email)
	if cmd.Email == "" {
		for {
			fmt.Print("Email: ")
			text, err := reader.ReadString('\n')
			if err != nil {
				return nil, err
			}
			cmd.Email = strings.TrimSpace(text)
			if cmd.Email == "" {
				fmt.Println("Email cannot be empty.")
				continue
			}
			exists, err := sq.FetchExists(cmd.Notebrew.DB, sq.CustomQuery{
				Dialect: cmd.Notebrew.Dialect,
				Format:  "SELECT 1 FROM users WHERE email = {email}",
				Values: []any{
					sq.StringParam("email", cmd.Email),
				},
			})
			if err != nil {
				return nil, err
			}
			if exists {
				fmt.Println("User already exists for the given email.")
				continue
			}
			break
		}
	}

	if cmd.PasswordHash == "" {
		for {
			fmt.Print("Password (will be hidden from view): ")
			password, err := term.ReadPassword(int(syscall.Stdin))
			fmt.Println()
			if err != nil {
				return nil, err
			}
			if utf8.RuneCount(password) < 8 {
				fmt.Println("Password must be at least 8 characters.")
				continue
			}
			fmt.Print("Confirm password (will be hidden from view): ")
			confirmPassword, err := term.ReadPassword(int(syscall.Stdin))
			fmt.Println()
			if err != nil {
				return nil, err
			}
			if subtle.ConstantTimeCompare(password, confirmPassword) != 1 {
				fmt.Fprintln(os.Stderr, "Passwords do not match.")
				continue
			}
			b, err := bcrypt.GenerateFromPassword(password, bcrypt.DefaultCost)
			if err != nil {
				return nil, err
			}
			cmd.PasswordHash = string(b)
			break
		}
	}

	return &cmd, nil
}

func (cmd *CreateUserCmd) Run() error {
	if cmd.Stderr == nil {
		cmd.Stderr = os.Stderr
	}
	tx, err := cmd.Notebrew.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = sq.Exec(tx, sq.CustomQuery{
		Dialect: cmd.Notebrew.Dialect,
		Format:  "INSERT INTO site (site_name) VALUES ({siteName})",
		Values: []any{
			sq.StringParam("siteName", cmd.Username),
		},
	})
	if err != nil {
		return err
	}
	userID := ulid.Make()
	_, err = sq.Exec(tx, sq.CustomQuery{
		Dialect: cmd.Notebrew.Dialect,
		Format: "INSERT INTO users (user_id, username, email, password_hash)" +
			" VALUES ({userID}, {username}, {email}, {passwordHash})",
		Values: []any{
			sq.UUIDParam("userID", userID),
			sq.StringParam("username", cmd.Username),
			sq.StringParam("email", cmd.Email),
			sq.StringParam("passwordHash", cmd.PasswordHash),
		},
	})
	if err != nil {
		return err
	}
	err = tx.Commit()
	if err != nil {
		return err
	}
	fmt.Fprintf(cmd.Stderr, "user created successfully\n")
	return nil
}
