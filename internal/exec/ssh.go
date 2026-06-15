// SSH executor. Authentication uses the SSH agent (SSH_AUTH_SOCK) and, if
// configured, a private key file.
package exec

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"strconv"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// Client is a reusable SSH connection implementing Executor. It is safe to call
// RunStream/Upload sequentially; each opens its own session.
type Client struct {
	cfg Config

	mu  sync.Mutex
	cli *ssh.Client
}

// newSSH returns an unconnected SSH client. Use exec.New to pick an executor.
func newSSH(cfg Config) *Client {
	if cfg.Port == 0 {
		cfg.Port = 22
	}
	return &Client{cfg: cfg}
}

func authMethods(keyPath string) ([]ssh.AuthMethod, error) {
	// An explicit key takes precedence and is used exclusively (like
	// `ssh -o IdentitiesOnly=yes`). Mixing in agent keys risks exhausting the
	// server's MaxAuthTries with unrelated keys before the right one is tried.
	if keyPath != "" {
		b, err := os.ReadFile(keyPath)
		if err != nil {
			return nil, fmt.Errorf("read key %s: %w", keyPath, err)
		}
		signer, err := ssh.ParsePrivateKey(b)
		if err != nil {
			return nil, fmt.Errorf("parse key %s: %w", keyPath, err)
		}
		return []ssh.AuthMethod{ssh.PublicKeys(signer)}, nil
	}

	// Otherwise fall back to the SSH agent.
	if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
		if conn, err := net.Dial("unix", sock); err == nil {
			return []ssh.AuthMethod{ssh.PublicKeysCallback(agent.NewClient(conn).Signers)}, nil
		}
	}

	return nil, fmt.Errorf("no SSH auth available: start ssh-agent or set deployment.key_path")
}

// Connect establishes the SSH connection if not already connected.
func (c *Client) Connect(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cli != nil {
		return nil
	}
	if c.cfg.Host == "" {
		return fmt.Errorf("deployment host is not configured")
	}

	methods, err := authMethods(c.cfg.KeyPath)
	if err != nil {
		return err
	}

	hostKeyCB := ssh.InsecureIgnoreHostKey()
	if !c.cfg.AcceptUnknownHostKey {
		hostKeyCB = ssh.InsecureIgnoreHostKey() // TODO: wire known_hosts verification
	}

	conf := &ssh.ClientConfig{
		User:            c.cfg.User,
		Auth:            methods,
		HostKeyCallback: hostKeyCB,
		Timeout:         15 * time.Second,
	}

	addr := net.JoinHostPort(c.cfg.Host, strconv.Itoa(c.cfg.Port))
	d := net.Dialer{Timeout: 15 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("dial %s: %w", addr, err)
	}
	sshConn, chans, reqs, err := ssh.NewClientConn(conn, addr, conf)
	if err != nil {
		conn.Close()
		return fmt.Errorf("ssh handshake %s: %w", addr, err)
	}
	c.cli = ssh.NewClient(sshConn, chans, reqs)
	return nil
}

// Connected reports whether an SSH connection is currently established.
func (c *Client) Connected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cli != nil
}

// Close tears down the connection.
func (c *Client) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cli != nil {
		c.cli.Close()
		c.cli = nil
	}
}

func (c *Client) client() (*ssh.Client, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cli == nil {
		return nil, fmt.Errorf("not connected")
	}
	return c.cli, nil
}

// RunStream runs cmd on the deployment host, writing each line of combined
// stdout/stderr to out. It returns when the command exits. The caller owns out
// and must not close it while RunStream is running. If ctx is cancelled the
// remote session is closed.
func (c *Client) RunStream(ctx context.Context, cmd string, out chan<- string) error {
	cli, err := c.client()
	if err != nil {
		return err
	}
	sess, err := cli.NewSession()
	if err != nil {
		return fmt.Errorf("new session: %w", err)
	}
	defer sess.Close()

	stdout, err := sess.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := sess.StderrPipe()
	if err != nil {
		return err
	}

	if err := sess.Start(cmd); err != nil {
		return fmt.Errorf("start: %w", err)
	}

	// Close the session if the context is cancelled.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			sess.Close()
		case <-done:
		}
	}()

	var wg sync.WaitGroup
	scan := func(r *bufio.Scanner) {
		defer wg.Done()
		r.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for r.Scan() {
			select {
			case out <- r.Text():
			case <-ctx.Done():
				return
			}
		}
	}
	wg.Add(2)
	go scan(bufio.NewScanner(stdout))
	go scan(bufio.NewScanner(stderr))
	wg.Wait()

	return sess.Wait()
}

// Upload writes content to remotePath on the deployment host (creating parent
// directories) by piping it through a remote shell.
func (c *Client) Upload(ctx context.Context, content []byte, remotePath string) error {
	cli, err := c.client()
	if err != nil {
		return err
	}
	sess, err := cli.NewSession()
	if err != nil {
		return err
	}
	defer sess.Close()

	stdin, err := sess.StdinPipe()
	if err != nil {
		return err
	}
	// mkdir -p the parent dir, then cat stdin to the file.
	cmd := fmt.Sprintf("mkdir -p \"$(dirname %q)\" && cat > %q", remotePath, remotePath)
	if err := sess.Start(cmd); err != nil {
		return err
	}
	if _, err := stdin.Write(content); err != nil {
		return err
	}
	stdin.Close()
	return sess.Wait()
}
