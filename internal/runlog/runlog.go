// Package runlog persists deployment run output to disk: one file per step plus
// a combined run.log, under a timestamped directory (logs/<YYYYMMDD-HHMMSS>/).
// A nil *Logger is valid and turns every method into a no-op, so callers can
// pass nil when logging is disabled without nil-checks.
package runlog

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Logger writes a run's output to a timestamped directory.
type Logger struct {
	dir string
	mu  sync.Mutex
	run *os.File
}

// New creates logs/<timestamp>/ under baseDir and opens run.log.
func New(baseDir string) (*Logger, error) {
	dir := filepath.Join(baseDir, time.Now().Format("20060102-150405"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	run, err := os.Create(filepath.Join(dir, "run.log"))
	if err != nil {
		return nil, err
	}
	return &Logger{dir: dir, run: run}, nil
}

// Dir returns the timestamped run directory.
func (l *Logger) Dir() string {
	if l == nil {
		return ""
	}
	return l.dir
}

// Event records a non-step line (step start/result, run boundaries) in run.log.
func (l *Logger) Event(format string, a ...any) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	fmt.Fprintf(l.run, "%s %s\n", time.Now().Format("15:04:05"), fmt.Sprintf(format, a...))
}

// Step opens a per-step log file and returns a line sink and a close func.
// Lines are written to <dir>/<id>.log and mirrored (prefixed) into run.log.
// On a nil Logger both returned funcs are no-ops.
func (l *Logger) Step(id string) (write func(string), closeStep func()) {
	if l == nil {
		return func(string) {}, func() {}
	}
	f, _ := os.Create(filepath.Join(l.dir, id+".log"))
	write = func(s string) {
		ts := time.Now().Format("15:04:05")
		if f != nil {
			fmt.Fprintf(f, "%s %s\n", ts, s)
		}
		l.mu.Lock()
		fmt.Fprintf(l.run, "%s [%s] %s\n", ts, id, s)
		l.mu.Unlock()
	}
	closeStep = func() {
		if f != nil {
			f.Close()
		}
	}
	return write, closeStep
}

// Close closes run.log.
func (l *Logger) Close() {
	if l == nil || l.run == nil {
		return
	}
	l.run.Close()
}
