// Package phpbridge runs the embedded PHP helper script (bootstrap.php) for the
// parts of a static content deployment that can only be done reliably in PHP.
package phpbridge

import (
	"bufio"
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
)

//go:embed bootstrap.php
var bootstrapScript []byte

// Bridge knows how to execute the PHP helper inside a Magento root.
type Bridge struct {
	PHP        string
	Root       string
	WithDB     bool
	scriptPath string
}

// New writes the embedded helper script to <root>/var/.go-static-deploy-<pid>/bootstrap.php.
// The script is written inside the Magento root so it also works with PHP running
// in a container that only has the Magento root mounted.
func New(php, root string, withDB bool) (*Bridge, error) {
	dir := filepath.Join(root, "var", fmt.Sprintf(".go-static-deploy-%d", os.Getpid()))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("creating %s: %w", dir, err)
	}
	scriptPath := filepath.Join(dir, "bootstrap.php")
	if err := os.WriteFile(scriptPath, bootstrapScript, 0o644); err != nil {
		return nil, fmt.Errorf("writing %s: %w", scriptPath, err)
	}
	return &Bridge{PHP: php, Root: root, WithDB: withDB, scriptPath: scriptPath}, nil
}

func (b *Bridge) args(command string) []string {
	args := []string{b.scriptPath, b.Root, command}
	if b.WithDB {
		args = append(args, "--with-db")
	}
	return args
}

// Run executes a one-shot command and decodes its JSON output into v.
func (b *Bridge) Run(command string, v any) error {
	cmd := exec.Command(b.PHP, b.args(command)...)
	cmd.Dir = b.Root
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("php %s failed: %w\n%s", command, err, strings.TrimSpace(stderr.String()))
	}
	if err := json.Unmarshal(stdout.Bytes(), v); err != nil {
		return fmt.Errorf("php %s returned invalid JSON: %w\n%s", command, err, truncate(stdout.String(), 500))
	}
	return nil
}

// Cleanup removes the helper script directory.
func (b *Bridge) Cleanup() {
	_ = os.RemoveAll(filepath.Dir(b.scriptPath))
}

// Response is a generic worker response.
type Response struct {
	ID      int64  `json:"id"`
	OK      bool   `json:"ok"`
	Error   string `json:"error"`
	Content string `json:"content"`
	Count   int    `json:"count"`
}

type worker struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	stderr *bytes.Buffer
}

// Pool is a pool of long running PHP worker processes. Starting PHP only once per
// worker avoids paying the PHP and autoloader start-up cost for every LESS file.
type Pool struct {
	bridge  *Bridge
	workers chan *worker
	all     []*worker
	nextID  atomic.Int64
	mu      sync.Mutex
	size    int
	started int
}

// NewPool creates a pool with at most size workers. Workers are started lazily.
func (b *Bridge) NewPool(size int) *Pool {
	if size < 1 {
		size = 1
	}
	return &Pool{bridge: b, workers: make(chan *worker, size), size: size}
}

func (p *Pool) startWorker() (*worker, error) {
	cmd := exec.Command(p.bridge.PHP, p.bridge.args("worker")...)
	cmd.Dir = p.bridge.Root
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr := &bytes.Buffer{}
	cmd.Stderr = &lockedWriter{w: stderr}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting PHP worker: %w", err)
	}
	return &worker{cmd: cmd, stdin: stdin, stdout: bufio.NewReaderSize(stdout, 1<<20), stderr: stderr}, nil
}

func (p *Pool) acquire() (*worker, error) {
	select {
	case w := <-p.workers:
		return w, nil
	default:
	}
	p.mu.Lock()
	if p.started < p.size {
		p.started++
		p.mu.Unlock()
		w, err := p.startWorker()
		if err != nil {
			p.mu.Lock()
			p.started--
			p.mu.Unlock()
			return nil, err
		}
		p.mu.Lock()
		p.all = append(p.all, w)
		p.mu.Unlock()
		return w, nil
	}
	p.mu.Unlock()
	return <-p.workers, nil
}

// Call sends a request to a free worker and waits for its response.
func (p *Pool) Call(request map[string]any) (*Response, error) {
	w, err := p.acquire()
	if err != nil {
		return nil, err
	}
	id := p.nextID.Add(1)
	request["id"] = id
	payload, err := json.Marshal(request)
	if err != nil {
		p.workers <- w
		return nil, err
	}
	payload = append(payload, '\n')
	if _, err := w.stdin.Write(payload); err != nil {
		return nil, p.workerFailed(w, err)
	}
	line, err := w.stdout.ReadBytes('\n')
	if err != nil {
		return nil, p.workerFailed(w, err)
	}
	p.workers <- w

	var resp Response
	if err := json.Unmarshal(line, &resp); err != nil {
		return nil, fmt.Errorf("invalid response from PHP worker: %w: %s", err, truncate(string(line), 300))
	}
	if !resp.OK {
		return &resp, fmt.Errorf("%s", resp.Error)
	}
	return &resp, nil
}

func (p *Pool) workerFailed(w *worker, err error) error {
	_ = w.cmd.Process.Kill()
	_ = w.cmd.Wait()
	p.mu.Lock()
	p.started--
	for i, candidate := range p.all {
		if candidate == w {
			p.all = append(p.all[:i], p.all[i+1:]...)
			break
		}
	}
	p.mu.Unlock()
	return fmt.Errorf("PHP worker died: %w\n%s", err, strings.TrimSpace(w.stderr.String()))
}

// Close stops all workers.
func (p *Pool) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, w := range p.all {
		_ = w.stdin.Close()
		_ = w.cmd.Wait()
	}
	p.all = nil
}

type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(b []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(b)
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}
