package exec

import (
	"bufio"
	"context"
	"os"
	osexec "os/exec"
	"path/filepath"
	"sync"
)

// LocalClient implements Executor by running commands on the local machine via
// `bash -c`. It is used when the CLI runs directly on the deployment node.
type LocalClient struct {
	connected bool
}

// NewLocal returns a local executor.
func NewLocal() *LocalClient { return &LocalClient{} }

// Connect is a no-op for local execution; it simply marks the executor ready.
func (l *LocalClient) Connect(ctx context.Context) error {
	l.connected = true
	return nil
}

// Connected reports whether Connect has been called.
func (l *LocalClient) Connected() bool { return l.connected }

// Close marks the executor not ready. There is nothing to release.
func (l *LocalClient) Close() { l.connected = false }

// RunStream runs cmd through bash on the local host, streaming combined
// stdout/stderr to out. It blocks until the command exits.
func (l *LocalClient) RunStream(ctx context.Context, cmd string, out chan<- string) error {
	c := osexec.CommandContext(ctx, "bash", "-c", cmd)

	stdout, err := c.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := c.StderrPipe()
	if err != nil {
		return err
	}
	if err := c.Start(); err != nil {
		return err
	}

	var wg sync.WaitGroup
	scan := func(s *bufio.Scanner) {
		defer wg.Done()
		s.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for s.Scan() {
			select {
			case out <- s.Text():
			case <-ctx.Done():
				return
			}
		}
	}
	wg.Add(2)
	go scan(bufio.NewScanner(stdout))
	go scan(bufio.NewScanner(stderr))
	wg.Wait()

	return c.Wait()
}

// Upload writes content to path on the local filesystem, creating parent
// directories as needed.
func (l *LocalClient) Upload(ctx context.Context, content []byte, path string) error {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(path, content, 0o644)
}
