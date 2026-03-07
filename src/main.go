package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/creack/pty"
)

// execCommand is a variable for mocking in tests
var execCommand = exec.Command

// lookPath is a variable for mocking in tests
var lookPath = exec.LookPath

const debug = false

type model struct {
	state           int
	prevState       int
	pwInput         textinput.Model
	dbInput         textinput.Model
	dbPath          string
	dbCompletions   []string
	dbCompletionIdx int
	err             string
	accounts        []string
	selected        int
	filter          string
	loading         bool
	otp             string
	countdown       int
	copied          bool
	width           int
	height          int
}

const (
	StatePassword = iota
	StateAccounts
	StateOTP
	StateHelp
	StateError
	StateDBPath
)

type errMsg struct {
	msg string
}

type accountsReadyMsg struct {
	accounts []string
}

type otpReadyMsg struct {
	otp string
}

type dbPathLoadedMsg struct {
	path string
}

type tickMsg struct{}

type quitMsg struct{}

func initialModel() model {
	pw := textinput.New()
	pw.Placeholder = "enter database password"
	pw.Focus()
	pw.EchoMode = textinput.EchoPassword
	pw.EchoCharacter = '•'
	pw.CharLimit = 64
	pw.Width = 30

	db := textinput.New()
	db.Placeholder = "~/Database.enc (tab accept, ↑↓ cycle)"
	db.CharLimit = 256
	db.Width = 50
	db.ShowSuggestions = true
	db.KeyMap.AcceptSuggestion = key.NewBinding(key.WithKeys("tab", "right"))

	return model{
		state:     StatePassword,
		prevState: StatePassword,
		pwInput:   pw,
		dbInput:   db,
	}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(checkDepsCmd(), loadConfigCmd())
}

func (m model) View() string {
	var b strings.Builder

	switch m.state {
	case StatePassword:
		b.WriteString("Enter your OTP database password:\n\n")
		b.WriteString(m.pwInput.View())
		if m.err != "" {
			b.WriteString("\n\nError: ")
			b.WriteString(m.err)
		}
	case StateDBPath:
		b.WriteString("First run - Enter full path to OTP database (.enc):\n\n")
		b.WriteString(m.dbInput.View())
		if m.err != "" {
			b.WriteString("\n\nError: ")
			b.WriteString(m.err)
		}
		b.WriteString("\n\nTab/right accepts suggestion, ↑↓ cycle, Enter to save & continue.")
	case StateAccounts:
		b.WriteString("Select account:\n\n")
		if m.loading {
			b.WriteString("Loading accounts...")
		} else if len(m.accounts) == 0 {
			b.WriteString("No accounts found.\n\nPress q to quit.")
		} else {
			filtered := filterAccounts(m.accounts, m.filter)
			if len(filtered) == 0 {
				b.WriteString("No matching accounts.\n")
			} else {
				// Use more vertical space for account list on small terminals (no borders)
				availLines := max(5, m.height-5)
				if availLines > len(filtered) {
					availLines = len(filtered)
				}
				startIdx := 0
				if len(filtered) > availLines {
					half := availLines / 2
					startIdx = clamp(m.selected-half, 0, len(filtered)-availLines)
				}
				// indicator if more above
				if startIdx > 0 {
					b.WriteString("...\n")
				}
				for i := startIdx; i < startIdx+availLines && i < len(filtered); i++ {
					account := filtered[i]
					maxLen := min(m.width-12, 60) // truncate long accounts for small terminals
					if len(account) > maxLen {
						account = truncate(account, maxLen)
					}
					if i == m.selected {
						b.WriteString(lipgloss.NewStyle().
							Bold(true).
							Foreground(lipgloss.Color("#A2C4E0")).
							Render("→ "+account) + "\n")
					} else {
						b.WriteString("  " + account + "\n")
					}
				}
				// indicator if more below
				if startIdx+availLines < len(filtered) {
					b.WriteString("...\n")
				}
				if m.filter != "" {
					b.WriteString("\nfilter: " + m.filter)
				}
			}
		}
	case StateOTP:
		b.WriteString("Your OTP:\n\n")
		otpStyle := lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#FFB6C1")).
			Padding(0, 5)
		b.WriteString(otpStyle.Render(m.otp))
		b.WriteString(fmt.Sprintf("\n\nRemaining: %2d s", m.countdown))
		if m.copied {
			b.WriteString("\n\n✅ Copied to clipboard!")
		}
	case StateHelp:
		b.WriteString("Help - Keybindings\n\n")
		b.WriteString("type to filter | / clear | ↑↓ tab/shift+tab nav | PgUp/Dn page | Enter select\n")
		b.WriteString("ESC: back/quit | ?: help | q/Ctrl+C: quit\n")
		b.WriteString("Password: Enter submit, Backspace delete\n")
		b.WriteString("OTP: any key exits, auto after 3s\n\n")
		b.WriteString("Press ESC/q/Ctrl+C to close.")

	case StateError:
		b.WriteString("Error:\n\n" + m.err + "\n\nPress ESC or Enter to continue.")
	}

	content := b.String()
	style := lipgloss.NewStyle().
		Padding(0, 1).
		Width(m.width)
	return lipgloss.Place(m.width, m.height, lipgloss.Left, lipgloss.Top, style.Render(content))
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		if m.width == 0 {
			m.width = 80
		}
		if m.height == 0 {
			m.height = 24
		}
	case tea.KeyMsg:
		k := msg.String()
		if m.state == StateOTP {
			return m, tea.Quit
		}
		if m.state == StateHelp {
			switch k {
			case "esc", "q", "ctrl+c":
				m.state = m.prevState
				return m, nil
			}
			return m, nil
		}
		if m.state == StatePassword {
			// Always update textinput first (standard Bubble Tea pattern), then handle special keys like enter.
			// This ensures enter key is processed reliably across terminals/expect.
			var cmd tea.Cmd
			m.pwInput, cmd = m.pwInput.Update(msg)
			k := msg.String()
			if k == "enter" || k == "\r" || k == "return" {
				pwStr := m.pwInput.Value()
				if pwStr == "" {
					m.err = "Password cannot be empty"
					return m, nil
				}
				m.err = ""
				m.loading = true
				pw := []byte(pwStr)
				return m, tea.Batch(cmd, fetchAccountsCmd(pw, m.dbPath)) // batch to handle both
			}
			if k == "esc" || k == "q" || k == "ctrl+c" {
				return m, tea.Quit
			}
			m.err = ""
			return m, cmd
		}
		if m.state == StateDBPath {
			// textinput with dynamic suggestions for ghost text (tab accepts current suggestion; ↑↓ cycle)
			var cmd tea.Cmd
			m.dbInput, cmd = m.dbInput.Update(msg)
			k := msg.String()

			cur := strings.TrimSpace(m.dbInput.Value())
			m.dbInput.SetSuggestions(getPathCompletions(cur))

			if k == "enter" || k == "\r" || k == "return" {
				path := strings.TrimSpace(m.dbInput.Value())
				path = expandTilde(path)
				if path == "" {
					m.err = "Path cannot be empty"
					return m, nil
				}
				if err := saveDBPath(path); err != nil {
					m.err = "Failed to save config: " + err.Error()
					return m, nil
				}
				m.dbPath = path
				m.err = ""
				m.state = StatePassword
				m.pwInput.Focus()
				m.dbCompletions = nil // reset
				m.dbCompletionIdx = 0
				return m, cmd
			}
			if k == "esc" || k == "q" || k == "ctrl+c" {
				return m, tea.Quit
			}
			if len(k) == 1 && k[0] >= 32 && k[0] < 127 {
				m.dbCompletions = nil
				m.dbCompletionIdx = 0
				m.err = ""
			}
			m.err = ""
			return m, cmd
		}
		switch k {
		case "esc":
			if m.state == StateAccounts {
				if m.filter != "" {
					m.filter = ""
					m.selected = 0
					return m, nil
				}
				// back to password
				m.state = StatePassword
				m.accounts = nil
				m.filter = ""
				m.selected = 0
				m.err = ""
				m.pwInput.SetValue("")
				m.pwInput.Focus()
				return m, nil
			}
			return m, tea.Quit
		case "enter", "\r":
			switch m.state {
			case StateAccounts:
				filtered := filterAccounts(m.accounts, m.filter)
				if m.loading || len(filtered) == 0 {
					return m, nil
				}
				selectedAcc := filtered[m.selected]
				// parse "Account | Issuer" for correct -a/-i
				parts := strings.SplitN(selectedAcc, " | ", 2)
				acc := strings.TrimSpace(parts[0])
				iss := ""
				if len(parts) > 1 {
					iss = strings.TrimSpace(parts[1])
				}
				m.loading = true
				pw := []byte(m.pwInput.Value())
				return m, fetchOTPcmd(pw, acc, iss, m.dbPath)
			case StateError:
				m.err = ""
				if m.prevState != 0 {
					m.state = m.prevState
				} else {
					m.state = StatePassword
				}
				m.pwInput.SetValue("")
				m.pwInput.Focus()
				return m, nil

			}
		case "backspace":
			if m.state == StatePassword {
				var cmd tea.Cmd
				m.pwInput, cmd = m.pwInput.Update(msg)
				m.err = ""
				return m, cmd
			}
			if m.state == StateAccounts {
				if len(m.filter) > 0 {
					m.filter = m.filter[:len(m.filter)-1]
					m.selected = 0
					return m, nil
				}
			}
		case "up":
			if m.state == StateAccounts && !m.loading {
				filtered := filterAccounts(m.accounts, m.filter)
				if len(filtered) > 0 {
					m.selected = clamp(m.selected-1, 0, len(filtered)-1)
				}
			}
		case "down":
			if m.state == StateAccounts && !m.loading {
				filtered := filterAccounts(m.accounts, m.filter)
				if len(filtered) > 0 {
					m.selected = clamp(m.selected+1, 0, len(filtered)-1)
				}
			}
		case "tab":
			if m.state == StateAccounts && !m.loading {
				filtered := filterAccounts(m.accounts, m.filter)
				if len(filtered) > 0 {
					m.selected = clamp(m.selected+1, 0, len(filtered)-1)
				}
			}
		case "shift+tab", "backtab":
			if m.state == StateAccounts && !m.loading {
				filtered := filterAccounts(m.accounts, m.filter)
				if len(filtered) > 0 {
					m.selected = clamp(m.selected-1, 0, len(filtered)-1)
				}
			}
		case "pageup", "pgup":
			if m.state == StateAccounts && !m.loading {
				filtered := filterAccounts(m.accounts, m.filter)
				pageSize := max(1, m.height/3)
				m.selected = clamp(m.selected-pageSize, 0, len(filtered)-1)
			}
		case "pagedown", "pgdn":
			if m.state == StateAccounts && !m.loading {
				filtered := filterAccounts(m.accounts, m.filter)
				pageSize := max(1, m.height/3)
				m.selected = clamp(m.selected+pageSize, 0, len(filtered)-1)
			}
		case "/":
			if m.state == StateAccounts {
				m.filter = ""
				m.selected = 0
				return m, nil
			}
		default:
			switch m.state {
			case StatePassword:
				var cmd tea.Cmd
				m.pwInput, cmd = m.pwInput.Update(msg)
				m.err = ""
				return m, cmd
			case StateAccounts:
				if len(msg.Runes) > 0 && len(msg.String()) == 1 && msg.String() != "/" {
					m.filter += string(msg.Runes[0])
					m.selected = 0
				}
			}
		}
	case accountsReadyMsg:
		m.accounts = msg.accounts
		m.loading = false
		m.state = StateAccounts
		m.selected = 0
		m.filter = ""
		if len(m.accounts) == 0 {
			m.err = "No accounts found"
		}
	case otpReadyMsg:
		log.Printf("otpReadyMsg received: otp=%q", msg.otp)
		m.otp = msg.otp
		m.countdown = computeRemaining()
		m.copied = true
		m.loading = false
		m.state = StateOTP
		// securely wipe password (textinput value cleared; full mem zeroing limited with string)
		m.pwInput.SetValue("")
		return m, tea.Batch(copyToClipboard(m.otp), spawnClearDaemonCmd(), tickCmd(), quitCmd(3*time.Second))
	case tickMsg:
		m.countdown = computeRemaining()
		return m, tickCmd()
	case quitMsg:
		return m, tea.Quit
	case errMsg:
		m.err = msg.msg
		m.loading = false
		if strings.Contains(strings.ToLower(msg.msg), "password") || strings.Contains(strings.ToLower(msg.msg), "incorrect") {
			m.state = StatePassword
			m.pwInput.SetValue("")
			m.pwInput.Focus()
		} else {
			m.prevState = m.state
			m.state = StateError
		}
	case dbPathLoadedMsg:
		m.dbPath = msg.path
		if m.dbPath == "" {
			m.state = StateDBPath
			m.dbInput.Focus()
			m.err = "Please enter path to your OTP database (.enc file)"
		} else {
			m.state = StatePassword
			m.pwInput.Focus()
		}
	}
	return m, cmd
}

func clamp(val, minv, maxv int) int {
	if val < minv {
		return minv
	}
	if val > maxv {
		return maxv
	}
	return val
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen < 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}

// fuzzyMatch checks if all characters in filter appear in order (case-insensitive)
// in s. This provides better "fuzzy" search than simple substring match per specs.
func fuzzyMatch(s, filter string) bool {
	if filter == "" {
		return true
	}
	sLower := strings.ToLower(s)
	fLower := strings.ToLower(filter)
	fIdx := 0
	for _, c := range sLower {
		if fIdx < len(fLower) && c == rune(fLower[fIdx]) {
			fIdx++
		}
	}
	return fIdx == len(fLower)
}

func filterAccounts(accounts []string, filter string) []string {
	if filter == "" {
		return accounts
	}
	var filtered []string
	for _, account := range accounts {
		if fuzzyMatch(account, filter) {
			filtered = append(filtered, account)
		}
	}
	return filtered
}

func computeRemaining() int {
	return 30 - int(time.Now().Unix()%30)
}

// isValidLine filters out headers, separators, error messages, and password prompts from
// otpclient-cli output. Keeps only real account names or OTP codes.
func isValidLine(line string) bool {
	line = strings.TrimSpace(line)
	if line == "" {
		return false
	}
	lower := strings.ToLower(line)
	if strings.Contains(lower, "password") || strings.Contains(lower, "pass") ||
		strings.Contains(lower, "couldn't find") || strings.Contains(lower, "given account") ||
		strings.Contains(lower, "given issuer") || strings.Contains(lower, "issuer") {
		return false
	}
	// skip separator lines and headers
	if strings.Contains(line, "====") || strings.Contains(line, "----") ||
		strings.Contains(line, "Account | Issuer") ||
		strings.Trim(line, "=|- 	") == "" {
		return false
	}
	return true
}

// looksLikeOTP returns true for lines that appear to be an OTP code (5-10 digits).
func looksLikeOTP(s string) bool {
	s = strings.TrimSpace(s)
	if len(s) < 5 || len(s) > 10 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// extractOTP finds the first 5-10 consecutive digits in a line (handles "Current TOTP...: 123456" etc).
func extractOTP(line string) string {
	var num strings.Builder
	for _, c := range line {
		if c >= '0' && c <= '9' {
			num.WriteRune(c)
			if num.Len() >= 10 {
				break
			}
		} else if num.Len() > 0 {
			if num.Len() >= 5 {
				return num.String()
			}
			num.Reset()
		}
	}
	if num.Len() >= 5 {
		return num.String()
	}
	return ""
}

// checkDependencies verifies required tools are in PATH.
// Uses lookPath var for testability. Returns user-friendly error if missing.
func checkDependencies() error {
	if _, err := lookPath("otpclient-cli"); err != nil {
		return fmt.Errorf("otpclient-cli not found in PATH. Please install OTPClient CLI")
	}
	if _, err := lookPath("wl-copy"); err != nil {
		return fmt.Errorf("wl-copy not found in PATH. Please install wl-clipboard (Wayland)")
	}
	return nil
}

// getConfigDir ensures ~/.config/charm-otp/ exists and returns its path.
func getConfigDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".config", "charm-otp")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	return dir, nil
}

// loadDBPath reads the saved database path from config file. Returns "" if not set.
func loadDBPath() (string, error) {
	dir, err := getConfigDir()
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "dbpath")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// saveDBPath writes the database path to config file.
func saveDBPath(dbPath string) error {
	dir, err := getConfigDir()
	if err != nil {
		return err
	}
	path := filepath.Join(dir, "dbpath")
	return os.WriteFile(path, []byte(strings.TrimSpace(dbPath)+"\n"), 0644)
}

// expandTilde expands ~ to home dir if present.
func expandTilde(path string) string {
	if strings.HasPrefix(path, "~/") || path == "~" {
		home, err := os.UserHomeDir()
		if err == nil {
			if path == "~" {
				return home
			}
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

// getPathCompletions returns possible .enc files and dirs for suggestions/completion.
// Preserves ~ prefix in results for UX while expanding internally for Glob/Stat.
func getPathCompletions(base string) []string {
	origBase := base
	expanded := base
	if strings.HasPrefix(base, "~/") || base == "~" {
		home, err := os.UserHomeDir()
		if err == nil {
			if base == "~" {
				expanded = home
			} else {
				expanded = filepath.Join(home, base[2:])
			}
		}
	}
	if expanded == "" {
		expanded = "."
	}
	var dir, baseName string
	// handle trailing / or ~ for dir browse
	if strings.HasSuffix(origBase, "/") || origBase == "~" || origBase == "~/" {
		dir = expanded
		baseName = ""
	} else {
		dir = filepath.Dir(expanded)
		baseName = filepath.Base(expanded)
		if baseName == "." || baseName == "/" {
			baseName = ""
		}
	}
	pattern := filepath.Join(dir, baseName+"*")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil
	}
	var comps []string
	home, _ := os.UserHomeDir()
	for _, m := range matches {
		if strings.HasPrefix(m, expanded) {
			info, err := os.Stat(m)
			if err == nil && (info.IsDir() || strings.HasSuffix(strings.ToLower(m), ".enc")) {
				display := m
				if strings.HasPrefix(origBase, "~") && home != "" {
					display = strings.Replace(m, home, "~", 1)
				}
				if info.IsDir() {
					display += "/"
				}
				comps = append(comps, display)
			}
		}
	}
	return comps
}

func runCommandWithPassword(name string, args []string, password string) (string, error) {
	log.Printf("runCommandWithPassword: %s %v (pw len=%d)", name, args, len(password))
	c := execCommand(name, args...)
	ptmx, err := pty.Start(c)
	if err != nil {
		log.Printf("pty.Start error: %v", err)
		return "", err
	}
	defer ptmx.Close()

	// Capture all output, including what is read during prompt detection
	var buf bytes.Buffer

	// Wait for password prompt before sending (more reliable pty interaction)
	log.Println("waiting for password prompt...")
	for i := 0; i < 5; i++ { // retry a few times
		promptBuf := make([]byte, 512)
		n, err := ptmx.Read(promptBuf)
		if err == nil && n > 0 {
			prompt := string(promptBuf[:n])
			buf.Write(promptBuf[:n]) // capture prompt/output read
			log.Printf("read prompt chunk: %s", prompt)
			if strings.Contains(prompt, "password") || strings.Contains(prompt, "Password") || strings.Contains(prompt, "decryption") {
				log.Println("prompt detected, sending pw")
				_, _ = io.WriteString(ptmx, password+"\n")
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	log.Println("password sent via pty (after prompt wait)")

	// Capture remaining output
	done := make(chan struct{})
	go func() {
		io.Copy(&buf, ptmx)
		close(done)
	}()

	// Timeout to prevent hanging CLI calls
	waitDone := make(chan error, 1)
	go func() {
		waitDone <- c.Wait()
	}()

	select {
	case err = <-waitDone:
		<-done
		if err != nil {
			log.Printf("CLI exited with error, output: %s", buf.String())
			return buf.String(), err
		}
		log.Printf("CLI success, output len=%d: %s", len(buf.String()), buf.String())
		return buf.String(), nil
	case <-time.After(10 * time.Second):
		c.Process.Kill()
		<-done
		log.Printf("CLI timeout after 10s, partial output: %s", buf.String())
		return buf.String(), fmt.Errorf("otpclient-cli timeout (check password/DB). Partial output: %s", buf.String())
	}
}

func getAccounts(pw []byte, dbPath string) ([]string, error) {
	dbPath = expandTilde(dbPath)
	if dbPath == "" {
		dbPath = "src/NewDatabase.enc" // fallback
	}
	out, err := runCommandWithPassword("otpclient-cli", []string{"--list", "-d", dbPath}, string(pw))
	if err != nil {
		return nil, err
	}
	scanner := bufio.NewScanner(strings.NewReader(out))
	var accounts []string
	for scanner.Scan() {
		line := scanner.Text()
		if isValidLine(line) {
			accounts = append(accounts, strings.TrimSpace(line))
		}
	}
	if scanErr := scanner.Err(); scanErr != nil {
		return nil, scanErr
	}
	return accounts, nil
}

func getOTP(pw []byte, account, issuer, dbPath string) (string, error) {
	dbPath = expandTilde(dbPath)
	if dbPath == "" {
		dbPath = "src/NewDatabase.enc" // fallback
	}
	args := []string{"--show", "-a", account, "-d", dbPath}
	if issuer != "" {
		args = append(args, "-i", issuer)
	}
	out, err := runCommandWithPassword("otpclient-cli", args, string(pw))
	if err != nil {
		log.Printf("getOTP error for account=%q issuer=%q db=%q: %v | partial out=%q", account, issuer, dbPath, err, out)
		return "", err
	}
	log.Printf("getOTP raw out for %q|%q (len=%d): %q", account, issuer, len(out), out)
	scanner := bufio.NewScanner(strings.NewReader(out))
	var otp string
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		valid := isValidLine(line)
		extracted := extractOTP(trimmed)
		log.Printf("getOTP scan line=%q valid=%t extracted=%q", trimmed, valid, extracted)
		if valid {
			if extracted != "" {
				otp = extracted
				log.Printf("getOTP selected OTP: %s", otp)
				break
			} else if otp == "" {
				otp = trimmed // fallback
			}
		}
	}
	if scanErr := scanner.Err(); scanErr != nil {
		return "", scanErr
	}
	log.Printf("getOTP final result for %q|%q: %q", account, issuer, otp)
	return otp, nil
}

func fetchOTPcmd(pw []byte, account, issuer, dbPath string) tea.Cmd {
	return func() tea.Msg {
		otp, err := getOTP(pw, account, issuer, dbPath)
		if err != nil {
			return errMsg{msg: err.Error()}
		}
		return otpReadyMsg{otp: otp}
	}
}

func fetchAccountsCmd(pw []byte, dbPath string) tea.Cmd {
	return func() tea.Msg {
		log.Printf("fetchAccountsCmd: pw len=%d db=%s", len(pw), dbPath)
		accounts, err := getAccounts(pw, dbPath)
		if err != nil {
			log.Printf("getAccounts error: %v", err)
			return errMsg{msg: err.Error()}
		}
		log.Printf("getAccounts success: %d accounts", len(accounts))
		return accountsReadyMsg{accounts: accounts}
	}
}

func copyToClipboard(otp string) tea.Cmd {
	log.Printf("copyToClipboard called with otp=%q (len=%d)", otp, len(otp))
	return func() tea.Msg {
		c := execCommand("wl-copy")
		stdin, err := c.StdinPipe()
		if err != nil {
			log.Printf("wl-copy pipe err: %v", err)
			return nil
		}
		io.WriteString(stdin, otp)
		stdin.Close()
		if err := c.Run(); err != nil {
			log.Printf("wl-copy run err: %v", err)
		} else {
			log.Println("wl-copy success")
		}
		return nil
	}
}

func spawnClearDaemonCmd() tea.Cmd {
	return func() tea.Msg {
		// Spawn detached daemon to clear clipboard after 5s (survives TUI exit)
		c := execCommand("sh", "-c", `nohup sh -c 'sleep 5 && wl-copy -c >/dev/null 2>&1' >/dev/null 2>&1 &`)
		if err := c.Start(); err != nil {
			log.Printf("failed to spawn clipboard clear daemon: %v", err)
		}
		return nil
	}
}

func tickCmd() tea.Cmd {
	return tea.Every(time.Second, func(t time.Time) tea.Msg {
		return tickMsg{}
	})
}

func quitCmd(d time.Duration) tea.Cmd {
	return func() tea.Msg {
		time.Sleep(d)
		return quitMsg{}
	}
}

func loadConfigCmd() tea.Cmd {
	return func() tea.Msg {
		path, err := loadDBPath()
		if err != nil {
			log.Printf("loadDBPath error: %v", err)
			return dbPathLoadedMsg{path: ""}
		}
		return dbPathLoadedMsg{path: path}
	}
}

func checkDepsCmd() tea.Cmd {
	return func() tea.Msg {
		if err := checkDependencies(); err != nil {
			return errMsg{msg: err.Error()}
		}
		return nil // success, proceed to password prompt
	}
}

func main() {
	// Log runtime errors/debug to file (TUI-safe) - disabled for release
	if debug {
		logFile, err := os.OpenFile("debug.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
		if err == nil {
			log.SetOutput(logFile)
			defer logFile.Close()
		} else {
			log.SetOutput(os.Stderr)
		}
	} else {
		log.SetOutput(io.Discard)
	}
	log.Println("=== simple-otp-tui starting at", time.Now(), "===")

	p := tea.NewProgram(initialModel(), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Printf("fatal error: %v\n", err)
		os.Exit(1)
	}
}
