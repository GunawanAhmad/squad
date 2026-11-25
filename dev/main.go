// Command dev is the local hot-reload loop: Vite serves the UI with hot module
// reload, and the Go server is rebuilt and restarted when Go sources, SQL
// migrations, characters.json or .env change. `make dev` runs it; the Go server
// proxies everything but /api to Vite, so http://127.0.0.1:8080 is the only URL
// to open. Ctrl+C stops both children; the compose services stay up.
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	binDir    = ".tmp"
	binary    = ".tmp/squad"
	viteURL   = "http://127.0.0.1:5173"
	pollEvery = 250 * time.Millisecond // how often the source tree is re-read
	quietFor  = 400 * time.Millisecond // wait out a save burst before building
	stopGrace = 5 * time.Second        // SIGINT to a child, then SIGKILL

	dim     = "\x1b[2m"
	red     = "\x1b[31m"
	green   = "\x1b[32m"
	yellow  = "\x1b[33m"
	magenta = "\x1b[35m"
	cyan    = "\x1b[36m"
	reset   = "\x1b[0m"
)

// color is on only for a terminal, so piping the output stays clean.
var color = func() bool {
	fi, err := os.Stdout.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}()

var sayMu sync.Mutex

// say writes one timestamped line with a short tag. The mutex keeps the Vite
// and server output from interleaving in the middle of a line.
func say(tag, c, format string, args ...any) {
	sayMu.Lock()
	defer sayMu.Unlock()
	now := time.Now().Format("15:04:05")
	if color {
		fmt.Fprintf(os.Stdout, "%s%s %s%-3s%s %s\n", dim, now, c, tag, reset, fmt.Sprintf(format, args...))
		return
	}
	fmt.Fprintf(os.Stdout, "%s %-3s %s\n", now, tag, fmt.Sprintf(format, args...))
}

// sayBlock prints text under one prefixed heading, so compiler errors keep the
// tag on every line.
func sayBlock(tag, c, heading, text string) {
	say(tag, c, "%s", heading)
	for _, line := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		say(tag, c, "  %s", line)
	}
}

// prefixLines copies a child's output to the terminal one line at a time.
func prefixLines(tag, c string, r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 8<<10), 1<<20)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), " \t\r")
		if line != "" {
			say(tag, c, "%s", line)
		}
	}
}

// process is a child whose output is prefixed on the terminal. Each child gets
// its own process group, so one signal also reaches anything it spawned (the
// claude CLI, npm's node, pg_dump).
type process struct {
	cmd  *exec.Cmd
	done chan struct{}
	err  error
}

func start(tag, c, dir string, env []string, name string, args ...string) (*process, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if env != nil {
		cmd.Env = append(os.Environ(), env...)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	if err := cmd.Start(); err != nil {
		_ = pw.Close()
		return nil, err
	}
	p := &process{cmd: cmd, done: make(chan struct{})}
	go prefixLines(tag, c, pr)
	go func() {
		p.err = cmd.Wait()
		_ = pw.Close()
		close(p.done)
	}()
	return p, nil
}

// stop interrupts the whole process group and escalates if it does not leave.
func (p *process) stop() {
	if p == nil {
		return
	}
	_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGINT)
	select {
	case <-p.done:
	case <-time.After(stopGrace):
		_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
		<-p.done
	}
}

// stamp is one watched file's identity: enough to notice a save, cheap enough
// to poll the whole tree four times a second.
type stamp struct {
	mod  int64
	size int64
}

// skipDir keeps the walk off the noisy trees. frontend/ is Vite's job and web/
// is a build artifact.
var skipDir = map[string]bool{
	".git": true, ".tmp": true, "node_modules": true, "web": true,
	"frontend": true, "backups": true, "searxng": true, "docs": true,
}

// watched is what a restart needs: Go sources, the embedded migrations, the
// character list and secrets read at startup. Everything else is Vite's or the
// database's.
func watched(name string) bool {
	switch name {
	case "go.mod", "go.sum", "characters.json", ".env":
		return true
	}
	return strings.HasSuffix(name, ".go") || strings.HasSuffix(name, ".sql")
}

func snapshot() (map[string]stamp, error) {
	files := make(map[string]stamp, 128)
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // a file that vanishes mid-walk is just another change
		}
		if d.IsDir() {
			if path != "." && skipDir[d.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		if !watched(d.Name()) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		files[path] = stamp{mod: info.ModTime().UnixNano(), size: info.Size()}
		return nil
	})
	return files, err
}

// firstChange returns any path that was added, removed or saved.
func firstChange(prev, cur map[string]stamp) (string, bool) {
	for path, s := range cur {
		if prev[path] != s {
			return path, true
		}
	}
	for path := range prev {
		if _, ok := cur[path]; !ok {
			return path, true
		}
	}
	return "", false
}

// supervisor owns the server across rebuilds.
type supervisor struct {
	mu  sync.Mutex
	app *process
}

func (s *supervisor) build(ctx context.Context) error {
	out, err := exec.CommandContext(ctx, "go", "build", "-buildvcs=false", "-o", binary, ".").CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w\n%s", err, strings.TrimRight(string(out), "\n"))
	}
	return nil
}

// swapApp replaces the running server with the freshly built binary. The old
// process is stopped first so it is not still holding the port.
func (s *supervisor) swapApp() error {
	s.mu.Lock()
	old := s.app
	s.app = nil
	s.mu.Unlock()
	old.stop()

	app, err := start("app", green, "", []string{"DEV=1"}, binary)
	if err != nil {
		return fmt.Errorf("starting %s: %w", binary, err)
	}

	s.mu.Lock()
	s.app = app
	s.mu.Unlock()

	go func() {
		<-app.done
		s.mu.Lock()
		current := s.app == app
		s.mu.Unlock()
		if current {
			say("app", red, "the server exited (%v); it restarts on the next save", app.err)
		}
	}()
	return nil
}

func (s *supervisor) stopApp() {
	s.mu.Lock()
	app := s.app
	s.app = nil
	s.mu.Unlock()
	app.stop()
}

// waitFor polls a URL until it answers, so the proxy is not asked to forward
// before Vite is listening.
func waitFor(ctx context.Context, url string, timeout time.Duration) bool {
	client := &http.Client{Timeout: time.Second}
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); {
		select {
		case <-ctx.Done():
			return false
		case <-time.After(100 * time.Millisecond):
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return false
		}
		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			return true
		}
	}
	return false
}

// portBusy reports whether something already listens on addr, so a forgotten
// server gets one clear line instead of a fatal bind error further down.
func portBusy(addr string) bool {
	l, err := net.Listen("tcp", addr)
	if err == nil {
		_ = l.Close()
		return false
	}
	return errors.Is(err, syscall.EADDRINUSE)
}

// addr is where the app listens, for the banner: ADDR from the environment or
// .env, else the server's own default.
func addr() string {
	if v := os.Getenv("ADDR"); v != "" {
		return v
	}
	raw, err := os.ReadFile(".env")
	if err != nil {
		return "127.0.0.1:8080"
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok && strings.TrimSpace(k) == "ADDR" {
			return strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	return "127.0.0.1:8080"
}

// watch blocks until ctx is cancelled, rebuilding after each quiet save burst.
func (s *supervisor) watch(ctx context.Context, ui *process) error {
	prev, err := snapshot()
	if err != nil {
		return err
	}
	say("dev", dim, "watching %d files (.go, .sql, characters.json, .env)", len(prev))

	tick := time.NewTicker(pollEvery)
	defer tick.Stop()

	var pending string
	var seen time.Time
	for {
		select {
		case <-ctx.Done():
			say("dev", dim, "stopping")
			return nil
		case <-ui.done:
			return fmt.Errorf("vite exited: %v", ui.err)
		case <-tick.C:
		}

		cur, err := snapshot()
		if err != nil {
			continue
		}
		if path, ok := firstChange(prev, cur); ok {
			prev, pending, seen = cur, path, time.Now()
			continue
		}
		if pending == "" || time.Since(seen) < quietFor {
			continue
		}
		changed := pending
		pending = ""

		say("dev", yellow, "%s changed, rebuilding", changed)
		started := time.Now()
		if err := s.build(ctx); err != nil {
			sayBlock("app", red, "build failed; still serving the last build:", err.Error())
			continue
		}
		if err := s.swapApp(); err != nil {
			say("app", red, "%v", err)
			continue
		}
		say("dev", green, "restarted in %s", time.Since(started).Round(time.Millisecond))
	}
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "dev:", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()

	if _, err := os.Stat(filepath.Join("frontend", "node_modules")); err != nil {
		return errors.New("frontend/node_modules is missing; run `make dev` or `npm ci` in frontend/")
	}
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return err
	}

	say("dev", cyan, "starting vite and the go server")
	if portBusy(addr()) {
		say("dev", yellow, "%s is already in use; stop the old server or set ADDR in .env", addr())
	}
	ui, err := start("ui", magenta, "frontend", nil, "npm", "run", "dev")
	if err != nil {
		return fmt.Errorf("vite: %w", err)
	}
	defer ui.stop()

	ready := make(chan bool, 1)
	go func() { ready <- waitFor(ctx, viteURL, 30*time.Second) }()
	select {
	case ok := <-ready:
		if !ok {
			return fmt.Errorf("vite did not answer on %s", viteURL)
		}
	case <-ui.done:
		return fmt.Errorf("vite exited: %v", ui.err)
	case <-ctx.Done():
		return nil
	}

	s := &supervisor{}
	defer s.stopApp()

	say("dev", dim, "building the server")
	started := false
	if err := s.build(ctx); err != nil {
		sayBlock("app", red, "build failed; the server starts after the next save:", err.Error())
	} else if err := s.swapApp(); err != nil {
		say("app", red, "%v", err)
	} else {
		started = true
	}
	if started {
		say("dev", green, "open http://%s — go changes restart, vue changes hot-update", addr())
	}
	return s.watch(ctx, ui)
}
