package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"
)

// readSecret is used for terminal input without echo. Tests and piped input use in.
func promptInitialAdmin(ctx context.Context, s *store, in io.Reader, out io.Writer, readSecret func() (string, error)) error {
	exists, err := s.hasUsers(ctx)
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
		if _, _, _, err := normalizeUsername(username); err != nil {
			fmt.Fprintln(out, err)
			continue
		}
		fmt.Fprint(out, "Admin password (at least 8 characters): ")
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
		if err := validatePassword(password); err != nil {
			fmt.Fprintln(out, err)
			continue
		}
		u, err := s.createUser(ctx, username, password, true)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "Administrator %q created.\n", u.Username)
		return nil
	}
}
