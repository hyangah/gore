/*
Yet another Go REPL that works nicely. Featured with line editing, code completion and more.

Usage

When started, a prompt is shown waiting for input. Enter any statement or expression to proceed.
If an expression is given or any variables are assigned or defined, their data will be pretty-printed.

Some special functionalities are provided as commands, which starts with colons:

	:import <package path>  Imports a package
	:print                  Prints current source code
	:write [<filename>]     Writes out current code
	:read [<filename>]	Reads in the source code
	:reset			Resets the session
	:run			Runs the current source code
	:doc <target>           Shows documentation for an expression or package name given
	:help                   Lists commands
	:quit                   Quit the session
*/
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"

	"github.com/hyangah/gore/session" // temporary
	"github.com/mitchellh/go-homedir"
)

const version = "0.2.6"

var (
	flagAutoImport = flag.Bool("autoimport", false, "formats and adjusts imports automatically")
	flagExtFiles   = flag.String("context", "",
		"import packages, functions, variables and constants from external golang source files")
	flagPkg = flag.String("pkg", "", "specify a package where the session will be run inside")
)

func main() {
	flag.Parse()

	var extFiles []string
	if *flagExtFiles != "" {
		extFiles = strings.Split(*flagExtFiles, ",")
	}

	s := &session.Session{
		AutoImports: *flagAutoImport,
		DotPkg:      *flagPkg,
		ExtFiles:    extFiles,
	}
	if err := s.Init(); err != nil {
		errorf("failed to prepare a session: %v", err)
		os.Exit(1)
	}

	fmt.Printf("gore version %s  :help for help\n", version)

	rl := session.NewContLiner()
	defer rl.Close()

	var historyFile string
	home, err := homeDir()
	if err != nil {
		errorf("home: %s", err)
	} else {
		historyFile = filepath.Join(home, "history")

		f, err := os.Open(historyFile)
		if err != nil {
			if !os.IsNotExist(err) {
				errorf("%s", err)
			}
		} else {
			_, err := rl.ReadHistory(f)
			if err != nil {
				errorf("while reading history: %s", err)
			}
		}
	}

	rl.SetWordCompleter(s.CompleteWord)

	for {
		in, err := rl.Prompt()
		if err != nil {
			if err == io.EOF {
				break
			}
			fmt.Fprintf(os.Stderr, "fatal: %s", err)
			os.Exit(1)
		}

		if in == "" {
			continue
		}

		if err := rl.Reindent(); err != nil {
			fmt.Fprintf(os.Stderr, "error: %s\n", err)
			rl.Clear()
			continue
		}

		c := make(chan os.Signal, 1)
		go func() { <-c }()
		signal.Notify(c, os.Interrupt)
		err = s.Eval(in)
		if err != nil {
			if err == session.ErrContinue {
				signal.Stop(c)
				close(c)
				continue
			} else if err == session.ErrQuit {
				signal.Stop(c)
				close(c)
				break
			}
			fmt.Println(err)
		}
		signal.Stop(c)
		close(c)
		rl.Accepted()
	}

	if historyFile != "" {
		err := os.MkdirAll(filepath.Dir(historyFile), 0755)
		if err != nil {
			errorf("%s", err)
		} else {
			f, err := os.Create(historyFile)
			if err != nil {
				errorf("%s", err)
			} else {
				_, err := rl.WriteHistory(f)
				if err != nil {
					errorf("while saving history: %s", err)
				}
			}
		}
	}
}

func homeDir() (home string, err error) {
	home = os.Getenv("GORE_HOME")
	if home != "" {
		return
	}

	home, err = homedir.Dir()
	if err != nil {
		return
	}

	home = filepath.Join(home, ".gore")
	return
}

func errorf(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", args...)
}
