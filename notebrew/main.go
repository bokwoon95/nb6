package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/bokwoon95/nb6"
)

var open = func(address string) {}

func main() {
	// notebrew -dir -addr -multisite -db
	var dir, addr, multisite, db string
	flagset := flag.NewFlagSet("", flag.ContinueOnError)
	flagset.StringVar(&dir, "dir", "", "")
	flagset.StringVar(&addr, "addr", "", "")
	flagset.StringVar(&multisite, "multisite", "", "")
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
	nbrew, err := nb6.New(&nb6.LocalFS{RootDir: dir})
	if err != nil {
		exit(err)
	}
	defer nbrew.Close()
	args := flagset.Args()
	if len(args) > 0 {
		if nbrew.DB == nil {
			err = os.WriteFile(filepath.Join(dir, "database.txt"), []byte("sqlite"), 0644)
			if err != nil {
				exit(err)
			}
			nbrew, err = nb6.New(&nb6.LocalFS{RootDir: dir})
			if err != nil {
				exit(err)
			}
		}
		return
	}
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
