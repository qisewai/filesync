// Package localprocess owns the lifetime of a desktop's backend process.
package localprocess

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"

	"filemanager/client"
)

type Process struct {
	Client *client.Client
	cmd    *exec.Cmd
	owner  io.WriteCloser
	dir    string
	once   sync.Once
	err    error
}

// Start requires an explicit companion executable. It never falls back to an
// in-process filesystem implementation or a program found through PATH.
func Start(ctx context.Context, executable, root string) (*Process, error) {
	return start(ctx, executable, root, "")
}

// StartWritable starts a backend that exclusively owns a persistent mutation journal.
func StartWritable(ctx context.Context, executable, root, journal string) (*Process, error) {
	if journal == "" {
		return nil, fmt.Errorf("persistent journal required")
	}
	return start(ctx, executable, root, journal)
}

func start(ctx context.Context, executable, root, journal string) (*Process, error) {
	parent := os.Getenv("XDG_RUNTIME_DIR")
	dir, err := os.MkdirTemp(parent, "filemanager-")
	if err != nil {
		return nil, err
	}
	if err := protectRuntimeDir(dir); err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	socket := filepath.Join(dir, "backend.sock")
	p := &Process{dir: dir, Client: client.Unix(socket)}
	p.cmd = exec.Command(executable, "--socket", socket, "--root", root, "--owner-stdin")
	if journal != "" {
		p.cmd.Args = append(p.cmd.Args, "--journal", journal)
	}
	p.cmd.Stderr = os.Stderr
	p.owner, err = p.cmd.StdinPipe()
	if err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	stdout, err := p.cmd.StdoutPipe()
	if err != nil {
		p.owner.Close()
		os.RemoveAll(dir)
		return nil, err
	}
	if err = p.cmd.Start(); err != nil {
		p.owner.Close()
		stdout.Close()
		os.RemoveAll(dir)
		return nil, err
	}
	ready := make(chan error, 1)
	go func() {
		line, err := bufio.NewReader(stdout).ReadString('\n')
		if err == nil && line != "READY 1\n" {
			err = fmt.Errorf("unexpected backend handshake %q", line)
		}
		ready <- err
	}()
	select {
	case err = <-ready:
	case <-ctx.Done():
		err = ctx.Err()
	}
	if err != nil {
		p.Close()
		return nil, err
	}
	if _, err = p.Client.Info(ctx); err != nil {
		p.Close()
		return nil, err
	}
	return p, nil
}

func (p *Process) Close() error {
	p.once.Do(func() {
		p.owner.Close()
		p.Client.Close()
		p.err = p.cmd.Wait()
		os.RemoveAll(p.dir)
	})
	return p.err
}
