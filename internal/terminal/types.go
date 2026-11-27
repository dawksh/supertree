// Package terminal owns native PTYs independently of control daemons and UIs.
// Only an explicit launch/restart request can execute a saved definition.
package terminal

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/dawksh/supertree/internal/command"
)

const (
	ProtocolVersion = 1
	MaxTerminals    = 8
	MaxReceipts     = 128
	MaxMessage      = 8 << 20
	MaxRequest      = 128 << 10
	MaxDefinition   = 32 << 10
	MaxColumns      = 240
	MaxRows         = 100
	ScrollbackLines = 256
	// Admission reserves 64 MiB per retained emulator, including both screens,
	// bounded graphemes/history and the measured 4 MiB parser. This is not RSS.
	ReservationBytes = 64 << 20
)

type Definition struct {
	WorkspaceID string            `json:"workspace_id"`
	Executable  string            `json:"executable"`
	Args        []string          `json:"args"`
	Directory   string            `json:"directory"`
	Environment map[string]string `json:"environment"`
	Columns     int               `json:"columns"`
	Rows        int               `json:"rows"`
}

func (d Definition) Validate() error {
	if !identifier(d.WorkspaceID) || !filepath.IsAbs(d.Executable) || !filepath.IsAbs(d.Directory) || !sizeOK(d.Columns, d.Rows) {
		return failure("invalid_arguments", "launch requires workspace ID, absolute executable/cwd and a supported terminal size")
	}
	b, err := json.Marshal(d)
	if err != nil || len(b) > MaxDefinition || len(d.Args) > 256 || len(d.Environment) > 256 {
		return failure("invalid_arguments", "launch definition exceeds bounds")
	}
	for _, s := range append(append([]string{d.Executable, d.Directory}, d.Args...), d.WorkspaceID) {
		if !utf8.ValidString(s) || strings.ContainsRune(s, 0) {
			return failure("invalid_arguments", "launch contains invalid text")
		}
	}
	for k, v := range d.Environment {
		if k == "" || strings.ContainsAny(k, "=\x00") || strings.ContainsRune(v, 0) || !utf8.ValidString(k+v) {
			return failure("invalid_arguments", "invalid environment entry")
		}
	}
	return nil
}

type Launch struct {
	RequestID  string     `json:"request_id"`
	TerminalID string     `json:"terminal_id"`
	Generation int64      `json:"generation"`
	Definition Definition `json:"definition"`
}

type Target struct {
	TerminalID string `json:"terminal_id"`
	Generation int64  `json:"generation"`
}

// Info intentionally excludes argv and environment values. PID is diagnostic,
// never authority to signal/adopt a process after a host epoch changes.
type Info struct {
	TerminalID      string   `json:"terminal_id"`
	Generation      int64    `json:"generation"`
	WorkspaceID     string   `json:"workspace_id"`
	RequestID       string   `json:"request_id"`
	HostEpoch       string   `json:"host_epoch"`
	State           string   `json:"state"`
	PID             int      `json:"pid,omitempty"`
	ExitCode        *int     `json:"exit_code,omitempty"`
	Signal          int      `json:"signal,omitempty"`
	Error           string   `json:"error,omitempty"`
	EnvironmentKeys []string `json:"environment_keys"`
}

type Status struct {
	Directory     string   `json:"directory"`
	Version       int      `json:"version"`
	Namespace     string   `json:"namespace"`
	Epoch         string   `json:"epoch"`
	PID           int      `json:"pid"`
	Capabilities  []string `json:"capabilities"`
	Terminals     []Info   `json:"terminals"`
	ReservedBytes int64    `json:"reserved_bytes"`
}

// Request is private local transport, not an agent terminal-input primitive.
// Every connection starts with hello; mutations require its exact host epoch.
type Request struct {
	Version   int     `json:"version"`
	Namespace string  `json:"namespace"`
	Op        string  `json:"op"`
	Epoch     string  `json:"epoch,omitempty"`
	Launch    *Launch `json:"launch,omitempty"`
	Target    *Target `json:"target,omitempty"`
	Force     bool    `json:"force,omitempty"`
	Row       int     `json:"row,omitempty"`
	Rows      int     `json:"rows,omitempty"`
}

type Response struct {
	Status *Status        `json:"status,omitempty"`
	Info   *Info          `json:"info,omitempty"`
	Screen *Screen        `json:"screen,omitempty"`
	Error  *command.Error `json:"error,omitempty"`
}

func failure(code, message string) error { return &command.Error{Code: code, Message: message} }
func identifier(s string) bool {
	if len(s) == 0 || len(s) > 128 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("_.:-", r)) {
			return false
		}
	}
	return true
}
func sizeOK(w, h int) bool { return w >= 2 && w <= MaxColumns && h >= 2 && h <= MaxRows }
func token() string        { var b [16]byte; _, _ = rand.Read(b[:]); return hex.EncodeToString(b[:]) }
