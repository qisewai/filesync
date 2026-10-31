package localprocess

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"sync"

	"filemanager/sharing"
)

type ShareProcess struct {
	Ready sharing.Ready
	cmd   *exec.Cmd
	owner io.WriteCloser
	once  sync.Once
	err   error
	done  chan struct{}
}

func StartShare(ctx context.Context, executable, config string) (*ShareProcess, error) {
	p := &ShareProcess{cmd: exec.Command(executable, "--share", config, "--owner-stdin")}
	p.cmd.Stderr = os.Stderr
	var err error
	p.owner, err = p.cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := p.cmd.StdoutPipe()
	if err != nil {
		p.owner.Close()
		return nil, err
	}
	if err = p.cmd.Start(); err != nil {
		p.owner.Close()
		out.Close()
		return nil, err
	}
	p.done = make(chan struct{})
	go func() { p.err = p.cmd.Wait(); close(p.done) }()
	ready := make(chan error, 1)
	go func() {
		line, err := bufio.NewReader(out).ReadBytes('\n')
		if err == nil {
			err = json.Unmarshal(line, &p.Ready)
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
	return p, nil
}
func (p *ShareProcess) Close() error {
	p.once.Do(func() { p.owner.Close() })
	return p.Wait()
}

func (p *ShareProcess) Done() <-chan struct{} { return p.done }
func (p *ShareProcess) Wait() error           { <-p.done; return p.err }
