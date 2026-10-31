package sharing

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"filemanager/service"
)

type Ready struct {
	Address     string `json:"address"`
	Fingerprint string `json:"fingerprint"`
}

// Run owns a separate service with only selected, read-only roots. The desktop
// private backend and its trusted-local handlers are never exposed here.
func Run(ctx context.Context, configPath string, ready io.Writer) error {
	c, err := Load(configPath)
	if err != nil {
		return err
	}
	if len(c.Key) < 32 || len(c.Key) > 256 || len(c.Roots) == 0 || len(c.Roots) > 64 {
		return errors.New("invalid sharing configuration")
	}
	for i := range c.Roots {
		id, err := strconv.ParseInt(c.Roots[i].ID, 10, 64)
		if err != nil || id <= 0 || strconv.FormatInt(id, 10) != c.Roots[i].ID {
			return errors.New("invalid shared root id")
		}
		c.Roots[i].ReadOnly = true
	}
	cert, pin, err := c.TLS()
	if err != nil {
		return err
	}
	s, err := service.New(c.Roots)
	if err != nil {
		return err
	}
	defer s.Close()
	// The invitation, TLS private key, app sessions, and adjacent config files
	// must not be downloadable even if the home directory itself is shared.
	protected, err := filepath.Abs(filepath.Dir(configPath))
	if err != nil {
		return err
	}
	protected, err = filepath.EvalSymlinks(protected)
	if err != nil {
		return err
	}
	rootPaths := map[string]string{}
	for _, r := range c.Roots {
		absolute, err := filepath.Abs(r.Path)
		if err != nil {
			return err
		}
		p, err := filepath.EvalSymlinks(absolute)
		if err != nil {
			return err
		}
		rootPaths[r.ID] = p
	}
	s.HiddenPath = func(id, p string) bool {
		absolute := filepath.Join(rootPaths[id], filepath.FromSlash(p))
		rel, err := filepath.Rel(protected, absolute)
		return err == nil && (rel == "." || filepath.IsLocal(rel))
	}
	l, err := net.Listen("tcp", c.Listen)
	if err != nil {
		return err
	}
	defer l.Close()
	var connections sync.WaitGroup
	server := &http.Server{ConnState: func(_ net.Conn, state http.ConnState) {
		switch state {
		case http.StateNew:
			connections.Add(1)
		case http.StateClosed, http.StateHijacked:
			connections.Done()
		}
	}, Handler: s.PeerHandler(c.Key), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10, BaseContext: func(net.Listener) context.Context { return ctx }}
	finished := make(chan error, 1)
	go func() {
		finished <- server.Serve(tls.NewListener(l, &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}))
	}()
	if err = json.NewEncoder(ready).Encode(Ready{Address: l.Addr().String(), Fingerprint: pin}); err != nil {
		server.Close()
		<-finished
		connections.Wait()
		return err
	}
	select {
	case err = <-finished:
		server.Close()
		connections.Wait()
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("sharing server: %w", err)
	case <-ctx.Done():
		server.Close()
		<-finished
		connections.Wait()
		return nil
	}
}
