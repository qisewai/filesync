// Package catalog owns persistent per-root tree indexes. It has no SQL or
// application dependency. Config, identity and authorization belong to callers.
package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"filemanager/index"
)

type Root struct {
	ID, Path string
	Excludes []string
}
type Catalog struct {
	mu     sync.Mutex
	hashMu sync.Mutex
	dir    string
	roots  map[string]*root
	closed bool
}
type root struct {
	closed bool
	mu     sync.Mutex
	config Root
	index  *index.Index
	log    *index.Journal
	file   string
	opts   index.ScanOptions
}

func New(dir string) (*Catalog, error) {
	var err error
	dir, err = filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	return &Catalog{dir: dir, roots: map[string]*root{}}, nil
}
func (c *Catalog) Close() {
	c.hashMu.Lock()
	defer c.hashMu.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	for _, r := range c.roots {
		r.mu.Lock()
		r.closed = true
		r.log.Close()
		r.mu.Unlock()
	}
}
func (c *Catalog) acquire(ctx context.Context, cfg Root) (*root, error) {
	c.mu.Lock()
	locked := true
	defer func() {
		if locked {
			c.mu.Unlock()
		}
	}()
	if c.closed {
		return nil, os.ErrClosed
	}
	if cfg.ID == "" || !filepath.IsAbs(cfg.Path) {
		return nil, errors.New("invalid catalog root")
	}
	cfg.Path = filepath.Clean(cfg.Path)
	cfg.Excludes = slices.Clone(cfg.Excludes)
	r := c.roots[cfg.ID]
	if r != nil && r.config.Path == cfg.Path && slices.Equal(r.config.Excludes, cfg.Excludes) {
		c.mu.Unlock()
		locked = false
		r.mu.Lock()
		if r.closed {
			r.mu.Unlock()
			return nil, os.ErrClosed
		}
		return r, nil
	}
	if r != nil {
		r.mu.Lock()
		r.closed = true
		r.log.Close()
		r.mu.Unlock()
		delete(c.roots, cfg.ID)
	}
	identity, _ := json.Marshal(cfg)
	digest := sha256.Sum256(identity)
	name := hex.EncodeToString(digest[:])
	file := filepath.Join(c.dir, name+".snapshot")
	log, err := index.OpenJournal(filepath.Join(c.dir, name+".journal"), digest)
	if err != nil {
		return nil, err
	}
	r = &root{config: cfg, index: index.New(), log: log, file: file, opts: index.ScanOptions{Roots: []string{cfg.Path}, Excludes: cfg.Excludes, Skip: []string{c.dir}}}
	if _, err = r.index.LoadSnapshot(ctx, file, r.opts, nil); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Close()
		return nil, err
	}
	// Reconcile recorded paths against disk; never reexecute file operations.
	var paths []string
	err = log.Replay(func(_ uint64, b []byte) error {
		var ps [][]byte
		if err := json.Unmarshal(b, &ps); err != nil {
			return err
		}
		for _, p := range ps {
			paths = append(paths, string(p))
		}
		return nil
	})
	if err == nil && len(paths) > 0 {
		err = r.index.Reconcile(ctx, r.opts, paths)
	}
	if err != nil {
		log.Close()
		return nil, err
	}
	c.roots[cfg.ID] = r
	r.mu.Lock()
	return r, nil
}
func (c *Catalog) Scan(ctx context.Context, cfg Root, full bool, progress func(index.Progress)) (index.Progress, error) {
	r, err := c.acquire(ctx, cfg)
	if err != nil {
		return index.Progress{}, err
	}
	defer r.mu.Unlock()
	var p index.Progress
	// A complete walk establishes the new base; overlays must not shadow it.
	// Catalog mutation reconciliation serializes on this same per-root lock.
	if full {
		p, err = r.index.Scan(ctx, r.opts, progress)
	} else {
		p, err = r.index.Update(ctx, r.opts, progress)
	}
	if err != nil {
		return p, err
	}
	if p.Errors == 0 {
		if err = r.index.ClearDeltas(); err != nil {
			return p, err
		}
	}
	if err = r.index.SaveSnapshot(ctx, r.file, r.opts, index.SnapshotMeta{Partial: p.Errors > 0, Errors: p.Errors, Messages: p.Messages}); err != nil {
		return p, err
	}
	// Publish the saved file-backed base while retaining any partial-scan
	// overlays and pinned query versions. Saving alone keeps all scan chunks.
	if _, err = r.index.LoadSnapshot(ctx, r.file, r.opts, nil); err != nil {
		return p, err
	}
	return p, nil
}
func (c *Catalog) Reconcile(ctx context.Context, cfg Root, relativePaths []string) error {
	r, err := c.acquire(ctx, cfg)
	if err != nil {
		return err
	}
	defer r.mu.Unlock()
	var paths []string
	var bytes [][]byte
	for _, p := range relativePaths {
		if p == "" {
			p = "."
		}
		if !filepath.IsLocal(p) {
			return errors.New("path outside catalog root")
		}
		abs := filepath.Join(cfg.Path, p)
		paths = append(paths, abs)
		bytes = append(bytes, []byte(abs))
	}
	b, err := json.Marshal(bytes)
	if err != nil {
		return err
	}
	if _, err = r.log.Append(b); err != nil {
		return err
	}
	return r.index.Reconcile(ctx, r.opts, paths)
}
func (c *Catalog) Search(ctx context.Context, cfg Root, q index.Query) (*index.Results, error) {
	r, err := c.acquire(ctx, cfg)
	if err != nil {
		return nil, err
	}
	defer r.mu.Unlock()
	return r.index.Search(ctx, q)
}
func (c *Catalog) Status(ctx context.Context, cfg Root) (index.Status, error) {
	r, err := c.acquire(ctx, cfg)
	if err != nil {
		return index.Status{}, err
	}
	defer r.mu.Unlock()
	return r.index.Status(), nil
}
func (c *Catalog) Subtree(ctx context.Context, cfg Root, path string) (index.Subtree, bool, error) {
	r, err := c.acquire(ctx, cfg)
	if err != nil {
		return index.Subtree{}, false, err
	}
	defer r.mu.Unlock()
	v, ok := r.index.Subtree(path)
	return v, ok, nil
}

// Record durably records affected paths before filesystem execution. Replaying
// intents only reconciles actual disk state; it never repeats a mutation.
func (c *Catalog) Record(ctx context.Context, cfg Root, relativePaths []string) error {
	r, err := c.acquire(ctx, cfg)
	if err != nil {
		return err
	}
	defer r.mu.Unlock()
	var paths [][]byte
	for _, p := range relativePaths {
		if p == "" {
			p = "."
		}
		if !filepath.IsLocal(p) {
			return errors.New("path outside catalog root")
		}
		paths = append(paths, []byte(filepath.Join(cfg.Path, p)))
	}
	b, err := json.Marshal(paths)
	if err != nil {
		return err
	}
	_, err = r.log.Append(b)
	return err
}
