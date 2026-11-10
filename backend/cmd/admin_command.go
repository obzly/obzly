package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"slices"
	"strings"

	"golang.org/x/term"

	"obzly/internal/features/audit_logs"
	users_services "obzly/internal/features/users/services"
)

// adminCommand is a one-off account repair run next to a live server, e.g.
// `docker exec obzly ./main --email=admin`. It runs before anything else starts
// (metrics listener, storage, broker, migrations), since the server in the same
// container already holds those ports and owns that work.
type adminCommand struct {
	Email               string
	NewPassword         string
	ResetMFA            bool
	ResetBootstrapToken bool
}

var adminFlagNames = []string{"email", "new-password", "reset-mfa", "reset-bootstrap-token"}

var errAdminUsage = errors.New("usage")

// parseAdminCommand reads the admin flags; ok is false when none was given, so
// the server starts as usual.
func parseAdminCommand(args []string, output io.Writer) (command adminCommand, ok bool, err error) {
	if !hasAdminFlag(args) {
		return adminCommand{}, false, nil
	}
	flags := flag.NewFlagSet("obzly", flag.ContinueOnError)
	flags.SetOutput(output)
	// --new-password still works for existing automation, but a password argument
	// shows in `ps` and shell history; --email alone prompts for it instead.
	flags.StringVar(&command.NewPassword, "new-password", "",
		"Set a new password for the user (insecure - visible via ps/shell history; "+
			"omit this and pass only --email to be prompted instead)")
	flags.StringVar(&command.Email, "email", "", "Email (or root admin login) of the user to reset")
	flags.BoolVar(&command.ResetMFA, "reset-mfa", false,
		"With --email: clear the user's MFA (lost authenticator) instead of resetting the password. "+
			"Admins set it up again at their next sign-in. Add --new-password to reset both.")
	flags.BoolVar(&command.ResetBootstrapToken, "reset-bootstrap-token", false,
		"Print a new bootstrap token for the root admin (the first one is logged only once, at creation). "+
			"Only while the root admin has no password.")
	if err := flags.Parse(args); err != nil {
		return adminCommand{}, true, err
	}
	if flags.NArg() > 0 {
		return adminCommand{}, true, fmt.Errorf("unexpected argument %q", flags.Arg(0))
	}
	if !command.ResetBootstrapToken && command.Email == "" {
		return adminCommand{}, true, fmt.Errorf("%w: pass --email <user email or root admin login>", errAdminUsage)
	}
	return command, true, nil
}

// runAdminCommand performs the command and returns the process exit code.
func runAdminCommand(command adminCommand, log *slog.Logger) int {
	// Resets are written to the audit log, like the same change made in the UI.
	audit_logs.SetupDependencies()
	users := users_services.GetUserService()

	if command.ResetBootstrapToken {
		token, err := users.ResetBootstrapToken()
		if err != nil {
			log.Error("Failed to reset the bootstrap token", "error", err)
			return 1
		}
		// stdout only, not the log, so it doesn't reach a log pipeline.
		fmt.Println("New root admin bootstrap token (the previous one no longer works):")
		fmt.Println(token)
		if command.Email == "" {
			return 0
		}
	}

	if command.ResetMFA {
		wasEnabled, err := users.ResetMFAByEmail(command.Email)
		if err != nil {
			log.Error("Failed to reset MFA", "error", err)
			return 1
		}
		if wasEnabled {
			log.Info("MFA reset - the user signs in with their password and, if an admin, sets up MFA again",
				"email", command.Email)
		} else {
			log.Info("MFA was not enabled for this user - cleared any unfinished setup", "email", command.Email)
		}
		if command.NewPassword == "" {
			return 0
		}
	}

	password := command.NewPassword
	if password == "" {
		var err error
		if password, err = promptForPassword(); err != nil {
			log.Error("Failed to read password", "error", err)
			return 1
		}
	}
	if err := users.ChangeUserPasswordByEmail(command.Email, password); err != nil {
		log.Error("Failed to reset password", "error", err)
		return 1
	}
	log.Info("Password reset successfully", "email", command.Email)
	return 0
}

// hasAdminFlag reports whether any admin flag is present, as -flag, --flag or
// --flag=value.
func hasAdminFlag(args []string) bool {
	for _, arg := range args {
		name, _, _ := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		if strings.HasPrefix(arg, "-") && slices.Contains(adminFlagNames, name) {
			return true
		}
	}
	return false
}

// promptForPassword reads a password from stdin with echo off, so it never
// shows on screen, in `ps` or in shell history.
func promptForPassword() (string, error) {
	fmt.Print("New password: ")
	passwordBytes, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println()
	if err != nil {
		return "", fmt.Errorf("failed to read password from terminal: %w", err)
	}

	password := string(passwordBytes)
	if password == "" {
		return "", fmt.Errorf("password must not be empty")
	}
	return password, nil
}
