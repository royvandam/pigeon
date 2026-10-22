package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/royvandam/pigeon/pkg/account"
	"github.com/royvandam/pigeon/pkg/quota"
)

// readSecret is used for terminal input without echo. Tests and piped input use in.
func promptInitialAdmin(ctx context.Context, accounts *account.Store, in io.Reader, out io.Writer, readSecret func() (string, error)) error {
	exists, err := accounts.HasUsers(ctx)
	if err != nil || exists {
		return err
	}
	fmt.Fprintln(out, "First run: create the initial administrator.")
	reader := bufio.NewReader(in)
	readLine := func() (string, error) {
		line, err := reader.ReadString('\n')
		if err != nil && (err != io.EOF || line == "") {
			return "", fmt.Errorf("initial administrator setup needs a username, password, and password confirmation on stdin: %w", err)
		}
		return strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r"), nil
	}
	passwordLine := readLine
	if readSecret != nil {
		passwordLine = func() (string, error) {
			password, err := readSecret()
			fmt.Fprintln(out)
			return password, err
		}
	}
	for {
		fmt.Fprint(out, "Admin username: ")
		username, err := readLine()
		if err != nil {
			return err
		}
		if _, _, _, err := account.NormalizeUsername(username); err != nil {
			fmt.Fprintln(out, err)
			continue
		}
		fmt.Fprint(out, "Admin password (at least 12 characters): ")
		password, err := passwordLine()
		if err != nil {
			return err
		}
		fmt.Fprint(out, "Confirm password: ")
		confirmation, err := passwordLine()
		if err != nil {
			return err
		}
		if password != confirmation {
			fmt.Fprintln(out, "Passwords do not match. Try again.")
			continue
		}
		if err := account.ValidatePassword(password); err != nil {
			fmt.Fprintln(out, err)
			continue
		}
		u, err := accounts.Create(ctx, username, password, true, quota.DefaultQuota)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "Administrator %q created.\n", u.Username)
		return nil
	}
}
