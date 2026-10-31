package service

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"runtime"
	"time"

	"filemanager/api"
)

// RunLocal serves a socket in a directory owned exclusively by the caller.
// It never unlinks a preexisting socket. Losing the owner pipe cancels ctx in
// the executable; all in-flight requests then stop before roots are closed.
func RunLocal(ctx context.Context, socket string, roots []api.Root, ready io.Writer, journal ...string) error {
	desktop := len(roots) == 1 && roots[0].ID == "local" && roots[0].Path == "/" && !roots[0].ReadOnly
	var s *Service
	var err error
	if desktop {
		s, err = newDesktopService()
	} else {
		s, err = New(roots)
	}
	if err != nil {
		return err
	}
	defer s.Close()
	if len(journal) > 0 && journal[0] != "" {
		if err := s.EnableWrites(journal[0]); err != nil {
			return err
		}
		if desktop {
			if err := s.EnableLocalBackup(); err != nil {
				return err
			}
			if err := s.EnableLocalIndex(); err != nil {
				return err
			}
			if err := s.EnableLocalTransfers(); err != nil {
				return err
			}
			if err := s.EnableDesktopTrash(); err != nil {
				return err
			}
		}
	}
	l, err := net.Listen("unix", socket)
	if err != nil {
		return err
	}
	defer l.Close()
	// Windows protects the parent directory with an explicit private ACL;
	// chmod is a Unix permission operation and cannot open AF_UNIX endpoints there.
	if runtime.GOOS != "windows" {
		if err := os.Chmod(socket, 0600); err != nil {
			return err
		}
	}
	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second, BaseContext: func(net.Listener) context.Context { return ctx }}
	finished := make(chan error, 1)
	go func() { finished <- srv.Serve(l) }()
	if _, err := io.WriteString(ready, "READY 1\n"); err != nil {
		srv.Close()
		<-finished
		return err
	}
	select {
	case err := <-finished:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		s.StopTasks()
		// No forced timeout: never kill a writer in the middle of commit.
		err := srv.Shutdown(context.Background())
		<-finished
		return err
	}
}
