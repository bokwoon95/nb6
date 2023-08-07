package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/bokwoon95/nb6"
	"github.com/bokwoon95/sq"
)

var open = func(address string) {}

func main() {
	var dir, addr, multisite, db, debug string
	flagset := flag.NewFlagSet("", flag.ContinueOnError)
	flagset.StringVar(&dir, "dir", "", "")
	flagset.StringVar(&addr, "addr", "", "")
	flagset.StringVar(&multisite, "multisite", "", "")
	flagset.StringVar(&db, "db", "", "")
	flagset.StringVar(&debug, "debug", "", "")
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
		dir = filepath.Join(userHomeDir, "notebrew-admin")
	}
	err = os.MkdirAll(dir, 0755)
	if err != nil {
		exit(err)
	}
	addr = strings.TrimSpace(addr)
	if addr != "" {
		if strings.Count(addr, ",") > 1 {
			exit(fmt.Errorf("-addr %q: too many commas (max 1)", addr))
		}
		err = os.WriteFile(filepath.Join(dir, "address.txt"), []byte(strings.ReplaceAll(addr, ",", "\n")), 0644)
		if err != nil {
			exit(err)
		}
	}
	multisite = strings.TrimSpace(multisite)
	if multisite != "" {
		err = os.WriteFile(filepath.Join(dir, "multisite.txt"), []byte(multisite), 0644)
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
	if debug != "" {
		isDebug, _ := strconv.ParseBool(debug)
		if isDebug {
			debug = "true"
		} else {
			debug = "false"
		}
		err = os.WriteFile(filepath.Join(dir, "debug.txt"), []byte(debug), 0644)
		if err != nil {
			exit(err)
		}
	}

	args := flagset.Args()
	if len(args) > 0 {
		command, args := args[0], args[1:]
		switch command {
		case "createuser":
			b, err := os.ReadFile(filepath.Join(dir, "database.txt"))
			if err != nil && !errors.Is(err, fs.ErrNotExist) {
				exit(err)
			}
			if len(bytes.TrimSpace(b)) == 0 {
				err = os.WriteFile(filepath.Join(dir, "database.txt"), []byte("sqlite"), 0644)
				if err != nil {
					exit(err)
				}
			}
			nbrew, err := NewNotebrew(dir)
			if err != nil {
				exit(err)
			}
			defer nbrew.Close()
			createUserCmd, err := CreateUserCommand(nbrew, args...)
			if err != nil {
				exit(fmt.Errorf(command+": %w", err))
			}
			err = createUserCmd.Run()
			if err != nil {
				exit(fmt.Errorf(command+": %w", err))
			}
		case "resetpassword":
			b, err := os.ReadFile(filepath.Join(dir, "database.txt"))
			if err != nil && !errors.Is(err, fs.ErrNotExist) {
				exit(err)
			}
			if len(bytes.TrimSpace(b)) == 0 {
				err = os.WriteFile(filepath.Join(dir, "database.txt"), []byte("sqlite"), 0644)
				if err != nil {
					exit(err)
				}
			}
			nbrew, err := NewNotebrew(dir)
			if err != nil {
				exit(err)
			}
			defer nbrew.Close()
			resetPasswordCmd, err := ResetPasswordCommand(nbrew, args...)
			if err != nil {
				exit(fmt.Errorf(command+": %w", err))
			}
			err = resetPasswordCmd.Run()
			if err != nil {
				exit(fmt.Errorf(command+": %w", err))
			}
		case "hashpassword":
			hashPasswordCmd, err := HashPasswordCommand(args...)
			if err != nil {
				exit(fmt.Errorf(command+": %w", err))
			}
			err = hashPasswordCmd.Run()
			if err != nil {
				exit(fmt.Errorf(command+": %w", err))
			}
		default:
			exit(fmt.Errorf("unknown command %s", command))
		}
		return
	}

	nbrew, err := NewNotebrew(dir)
	if err != nil {
		exit(err)
	}
	defer nbrew.Close()
	server, err := nbrew.NewServer()
	if err != nil {
		exit(err)
	}
	wait := make(chan os.Signal, 1)
	signal.Notify(wait, syscall.SIGINT, syscall.SIGTERM)
	if nbrew.Protocol == "https://" {
		go http.ListenAndServe(":80", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != "GET" && r.Method != "HEAD" {
				http.Error(w, "Use HTTPS", http.StatusBadRequest)
				return
			}
			host, _, err := net.SplitHostPort(r.Host)
			if err != nil {
				host = r.Host
			} else {
				host = net.JoinHostPort(host, "443")
			}
			http.Redirect(w, r, "https://"+host+r.URL.RequestURI(), http.StatusFound)
		}))
		fmt.Println("Listening on " + server.Addr)
		go server.ListenAndServeTLS("", "")
	} else {
		listener, err := net.Listen("tcp", server.Addr)
		if err != nil {
			var errno syscall.Errno
			if !errors.As(err, &errno) {
				exit(err)
			}
			// WSAEADDRINUSE copied from
			// https://cs.opensource.google/go/x/sys/+/refs/tags/v0.6.0:windows/zerrors_windows.go;l=2680
			// To avoid importing an entire 3rd party library just to use a constant.
			const WSAEADDRINUSE = syscall.Errno(10048)
			if errno == syscall.EADDRINUSE || runtime.GOOS == "windows" && errno == WSAEADDRINUSE {
				fmt.Println("http://" + server.Addr)
				open("http://" + server.Addr)
			}
			return
		}
		open("http://" + server.Addr)
		// NOTE: We may need to give a more intricate ASCII header in order for the
		// GUI double clickers to realize that the terminal window is important, so
		// that they won't accidentally close it thinking it is some random
		// terminal.
		fmt.Println("Listening on http://" + server.Addr)
		go server.Serve(listener)
	}
	<-wait
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	server.Shutdown(ctx)
}

func NewNotebrew(dir string) (*nb6.Notebrew, error) {
	nbrew, err := nb6.New(&nb6.LocalFS{RootDir: dir})
	if err != nil {
		return nil, err
	}
	// Configure the logger.
	logger := sq.NewLogger(nbrew.Stdout, "", log.LstdFlags, sq.LoggerConfig{
		ShowTimeTaken:     true,
		ShowCaller:        true,
		LogAsynchronously: true,
	})
	sq.SetDefaultLogSettings(func(ctx context.Context, logSettings *sq.LogSettings) {
		logger.SqLogSettings(ctx, logSettings)
	})
	sq.SetDefaultLogQuery(func(ctx context.Context, queryStats sq.QueryStats) {
		// If there was an error, always log the query unconditionally.
		if queryStats.Err != nil {
			logger.SqLogQuery(ctx, queryStats)
			return
		}
		// Otherwise, log the query depending on the contents inside debug.txt.
		file, err := nbrew.FS.Open("debug.txt")
		if err != nil {
			return
		}
		defer file.Close()
		reader := bufio.NewReader(file)
		b, _ := reader.Peek(6)
		if len(b) == 0 {
			return
		}
		debug, _ := strconv.ParseBool(string(bytes.TrimSpace(b)))
		if debug {
			logger.SqLogQuery(ctx, queryStats)
		}
	})
	sq.DefaultDialect.Store(&nbrew.Dialect)
	return nbrew, nil
}
