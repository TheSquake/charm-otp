package main

import (
	"os/exec"
	"strings"
	"testing"
)

func TestClamp(t *testing.T) {
	tests := []struct {
		val, minv, maxv, want int
	}{
		{5, 0, 10, 5},
		{-1, 0, 10, 0},
		{11, 0, 10, 10},
		{0, 0, 0, 0},
	}
	for _, tt := range tests {
		t.Run("", func(t *testing.T) {
			if got := clamp(tt.val, tt.minv, tt.maxv); got != tt.want {
				t.Errorf("clamp() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMax(t *testing.T) {
	tests := []struct {
		a, b, want int
	}{
		{1, 2, 2},
		{3, 2, 3},
		{5, 5, 5},
	}
	for _, tt := range tests {
		t.Run("", func(t *testing.T) {
			if got := max(tt.a, tt.b); got != tt.want {
				t.Errorf("max() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFilterAccounts(t *testing.T) {
	accounts := []string{"Google:foo", "Apple:bar", "GitHub:baz", "Google:another"}

	// empty filter returns all
	filtered := filterAccounts(accounts, "")
	if len(filtered) != 4 {
		t.Errorf("filterAccounts(empty) len=%d, want 4", len(filtered))
	}

	// basic match
	filtered = filterAccounts(accounts, "google")
	if len(filtered) != 2 || filtered[0] != "Google:foo" || filtered[1] != "Google:another" {
		t.Errorf("filterAccounts(google) = %v, want 2 items", filtered)
	}

	// fuzzy matches (chars in order, case-insensitive)
	filtered = filterAccounts(accounts, "goa")
	if len(filtered) != 1 || filtered[0] != "Google:another" {
		t.Errorf("filterAccounts(goa fuzzy) = %v, want Google:another", filtered)
	}

	filtered = filterAccounts(accounts, "gh")
	if len(filtered) != 2 || !strings.Contains(filtered[0], "GitHub") || !strings.Contains(filtered[1], "Google") {
		t.Errorf("filterAccounts(gh fuzzy) = %v, want GitHub and Google:another", filtered)
	}

	filtered = filterAccounts(accounts, "gl")
	if len(filtered) != 2 || !strings.Contains(filtered[0], "Google") {
		t.Errorf("filterAccounts(gl fuzzy) = %v, want 2 Google items", filtered)
	}

	// no match
	filtered = filterAccounts(accounts, "xyz")
	if len(filtered) != 0 {
		t.Errorf("filterAccounts(xyz) len=%d, want 0", len(filtered))
	}
}

func TestComputeRemaining(t *testing.T) {
	rem := computeRemaining()
	if rem < 0 || rem > 30 {
		t.Errorf("computeRemaining() = %d, want 0-30", rem)
	}
}

func TestCheckDependencies(t *testing.T) {
	origLookPath := lookPath
	defer func() { lookPath = origLookPath }()

	// success case
	lookPath = func(file string) (string, error) {
		return "/usr/bin/" + file, nil
	}
	if err := checkDependencies(); err != nil {
		t.Errorf("expected no error, got %v", err)
	}

	// missing otpclient-cli
	lookPath = func(file string) (string, error) {
		if file == "otpclient-cli" {
			return "", exec.ErrNotFound
		}
		return "/usr/bin/" + file, nil
	}
	if err := checkDependencies(); err == nil || !strings.Contains(err.Error(), "otpclient-cli") {
		t.Errorf("expected otpclient-cli error, got %v", err)
	}

	// missing wl-copy
	lookPath = func(file string) (string, error) {
		if file == "wl-copy" {
			return "", exec.ErrNotFound
		}
		return "/usr/bin/" + file, nil
	}
	if err := checkDependencies(); err == nil || !strings.Contains(err.Error(), "wl-copy") {
		t.Errorf("expected wl-copy error, got %v", err)
	}
}

func TestGetAccounts(t *testing.T) {
	origExec := execCommand
	origLook := lookPath
	defer func() {
		execCommand = origExec
		lookPath = origLook
	}()

	lookPath = func(file string) (string, error) { return "/usr/bin/" + file, nil }

	// mock successful list - include prompt so pty logic in runCommandWithPassword triggers correctly
	execCommand = func(name string, args ...string) *exec.Cmd {
		if name == "otpclient-cli" && len(args) > 0 && args[0] == "--list" {
			return exec.Command("echo", "-e", "Type the DB decryption password: \nTestAccount1\nGoogle:user@example.com\nGitHub:testuser\n")
		}
		return exec.Command(name, args...)
	}

	accounts, err := getAccounts([]byte("testpass"), "")
	if err != nil {
		t.Fatalf("getAccounts failed: %v", err)
	}
	if len(accounts) != 3 {
		t.Errorf("expected 3 accounts, got %d: %v", len(accounts), accounts)
	}
	if !strings.Contains(accounts[1], "Google") {
		t.Errorf("expected Google account, got %v", accounts)
	}
}

func TestGetAccountsError(t *testing.T) {
	origExec := execCommand
	origLook := lookPath
	defer func() {
		execCommand = origExec
		lookPath = origLook
	}()

	lookPath = func(file string) (string, error) { return "/usr/bin/" + file, nil }

	// mock error case (invalid password or CLI fail)
	execCommand = func(name string, args ...string) *exec.Cmd {
		if name == "otpclient-cli" {
			// cmd that prints error and exits non-zero
			return exec.Command("sh", "-c", `echo "Error: incorrect password"; exit 1`)
		}
		return exec.Command(name, args...)
	}

	_, err := getAccounts([]byte("wrongpass"), "")
	if err == nil {
		t.Error("expected error for invalid password")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "password") && !strings.Contains(err.Error(), "exit") {
		t.Errorf("expected password or exec error, got: %v", err)
	}
}

func TestGetOTP(t *testing.T) {
	origExec := execCommand
	origLook := lookPath
	defer func() {
		execCommand = origExec
		lookPath = origLook
	}()

	lookPath = func(file string) (string, error) { return "/usr/bin/" + file, nil }

	// mock for --show with prompt so pty logic triggers
	execCommand = func(name string, args ...string) *exec.Cmd {
		if name == "otpclient-cli" && len(args) > 1 && args[0] == "--show" {
			return exec.Command("echo", "-e", "Type the DB decryption password: \n123456\nPassword prompt ignored\n")
		}
		return exec.Command(name, args...)
	}

	otp, err := getOTP([]byte("testpass"), "Google:user@example.com", "", "")
	if err != nil {
		t.Fatalf("getOTP failed: %v", err)
	}
	if otp != "123456" {
		t.Errorf("expected OTP 123456, got %s", otp)
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		s, want string
		maxLen  int
	}{
		{"short", "short", 10},
		{"verylongaccountname", "verylongac...", 13},
		{"abc", "abc", 3},
		{"ab", "ab", 3},
	}
	for _, tt := range tests {
		if got := truncate(tt.s, tt.maxLen); got != tt.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tt.s, tt.maxLen, got, tt.want)
		}
	}
}
