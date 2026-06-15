// Package exec runs Genestack deployment commands and streams their combined
// stdout/stderr back line by line. It offers two executors selected at
// runtime: an SSH client (drive a remote deployment host from your laptop) and
// a local client (run directly on the deployment node).
package exec

import "context"

// Executor runs deployment commands on the deployment host and streams their
// combined stdout/stderr back line by line. Two implementations exist: an SSH
// client (driving a remote deployment host from your laptop) and a local
// client (when the CLI runs directly on the deployment node).
type Executor interface {
	// Connect prepares the executor for use. For SSH this dials the host; for
	// local execution it is effectively a no-op.
	Connect(ctx context.Context) error
	// Connected reports whether the executor is ready.
	Connected() bool
	// Close releases any resources (closes the SSH connection).
	Close()
	// RunStream runs cmd, writing each output line to out. It blocks until the
	// command exits. The caller owns out and must not close it during the call.
	RunStream(ctx context.Context, cmd string, out chan<- string) error
	// Upload writes content to path on the deployment host, creating parents.
	Upload(ctx context.Context, content []byte, path string) error
}

// Config describes how to reach the deployment host.
type Config struct {
	// Local runs commands on the local machine instead of over SSH.
	Local bool

	// SSH fields (ignored when Local is true).
	Host    string
	Port    int
	User    string
	KeyPath string
	// AcceptUnknownHostKey skips host key verification (lab/bootstrap use).
	AcceptUnknownHostKey bool
}

// New returns the appropriate Executor for the configuration.
func New(cfg Config) Executor {
	if cfg.Local {
		return NewLocal()
	}
	return newSSH(cfg)
}
