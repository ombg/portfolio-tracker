// Command ptctl is the admin CLI (D-1, D-32). It connects with DATABASE_URL as the database owner.
//
//	ptctl user create --email EMAIL --name NAME [--currency CAD]
//	ptctl user set-password --email EMAIL
//
// The password is prompted for twice on a terminal, or read as one line from standard input.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/ombg/portfolio-tracker/services/api/internal/admin"
	"github.com/ombg/portfolio-tracker/services/api/internal/db"
)

const usage = `usage:
  ptctl user create --email EMAIL --name NAME [--currency CAD|USD|EUR]
  ptctl user set-password --email EMAIL`

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "ptctl:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) < 2 || args[0] != "user" {
		return errors.New(usage)
	}
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		return errors.New("DATABASE_URL is required")
	}

	switch args[1] {
	case "create":
		fs := flag.NewFlagSet("user create", flag.ContinueOnError)
		email := fs.String("email", "", "login email")
		name := fs.String("name", "", "display name")
		currency := fs.String("currency", "CAD", "reporting currency")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		if *email == "" || *name == "" {
			return errors.New(usage)
		}
		pw, err := readPassword()
		if err != nil {
			return err
		}
		pool, err := db.Open(ctx, url)
		if err != nil {
			return err
		}
		defer pool.Close()
		id, err := admin.CreateUser(ctx, pool, admin.NewUser{Email: *email, DisplayName: *name, ReportingCurrency: *currency, Password: pw})
		if err != nil {
			return err
		}
		fmt.Println("created user", id)
		return nil

	case "set-password":
		fs := flag.NewFlagSet("user set-password", flag.ContinueOnError)
		email := fs.String("email", "", "login email")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		if *email == "" {
			return errors.New(usage)
		}
		pw, err := readPassword()
		if err != nil {
			return err
		}
		pool, err := db.Open(ctx, url)
		if err != nil {
			return err
		}
		defer pool.Close()
		if err := admin.SetPassword(ctx, pool, *email, pw); err != nil {
			return err
		}
		fmt.Println("password changed; existing sessions ended")
		return nil
	}
	return errors.New(usage)
}

func readPassword() (string, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		return strings.TrimRight(line, "\r\n"), nil
	}
	fmt.Fprint(os.Stderr, "Password: ")
	first, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	fmt.Fprint(os.Stderr, "Repeat password: ")
	second, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	if string(first) != string(second) {
		return "", errors.New("passwords do not match")
	}
	return string(first), nil
}
