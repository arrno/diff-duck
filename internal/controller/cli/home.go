package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// homeEnv overrides where DiffDuck keeps its database, server record, and log.
const homeEnv = "DIFFDUCK_HOME"

// home returns DiffDuck's own directory, creating it readable only by the user.
func home() (string, error) {
	dir := os.Getenv(homeEnv)
	if dir == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return "", fmt.Errorf("locate config directory (set %s): %w", homeEnv, err)
		}
		dir = filepath.Join(base, "diff-duck")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create %s: %w", dir, err)
	}
	return dir, nil
}

// serverRecord is what a running server writes so later commands can find it.
type serverRecord struct {
	PID  int    `json:"pid"`
	Port int    `json:"port"`
	DB   string `json:"db"`
	// Args are the serve flags it started with, so reset can start it again the same way.
	Args []string `json:"args,omitempty"`
}

func (r serverRecord) url() string { return fmt.Sprintf("http://127.0.0.1:%d/", r.Port) }

func recordPath(dir string) string { return filepath.Join(dir, "server.json") }
func logPath(dir string) string    { return filepath.Join(dir, "server.log") }
func defaultDB(dir string) string  { return filepath.Join(dir, "diffs.db") }

// readRecord returns nil when no server has recorded itself.
func readRecord(dir string) (*serverRecord, error) {
	data, err := os.ReadFile(recordPath(dir))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r serverRecord
	if err := json.Unmarshal(data, &r); err != nil || r.PID <= 0 || r.Port <= 0 {
		// A damaged record names no server, and the next server replaces it.
		return nil, nil
	}
	return &r, nil
}

func writeRecord(dir string, r serverRecord) error {
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".server-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), recordPath(dir))
}

// removeRecord removes the record only if it still names pid, so a server never erases
// another's.
func removeRecord(dir string, pid int) {
	if r, _ := readRecord(dir); r != nil && r.PID == pid {
		_ = os.Remove(recordPath(dir))
	}
}
