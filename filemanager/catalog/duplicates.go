package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"

	"filemanager/index"
)

type Fingerprint struct {
	RootPath       []byte
	Root           string
	Path           []byte
	Size, Modified int64
	Hash           string
}
type HashProgress struct {
	Files, Total, Bytes int64
	Current             string
}

// FindDuplicates derives candidates exclusively from pinned tree versions.
// A result is committed only after a full successful pass; canceled work leaves
// the previous result intact. Symlinks never participate in content hashing.
func (c *Catalog) FindDuplicates(ctx context.Context, roots []Root, minSize int64, progress func(HashProgress)) error {
	c.hashMu.Lock()
	defer c.hashMu.Unlock()
	type candidate struct {
		root  Root
		entry index.Entry
	}
	sizes := map[int64][]candidate{}
	for _, root := range roots {
		result, err := c.Search(ctx, root, index.Query{Hidden: true, Category: "file", MinSize: minSize})
		if err != nil {
			return err
		}
		if err := result.Each(ctx, func(e index.Entry) error {
			if !e.Link {
				sizes[e.Size] = append(sizes[e.Size], candidate{root, e})
			}
			return nil
		}); err != nil {
			return err
		}
	}
	var p HashProgress
	var out []Fingerprint
	for _, entries := range sizes {
		if len(entries) > 1 {
			p.Total += int64(len(entries))
		}
	}
	for _, entries := range sizes {
		if len(entries) < 2 {
			continue
		}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			root, err := os.OpenRoot(entry.root.Path)
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(entry.root.Path, entry.entry.Path)
			if err != nil {
				root.Close()
				return err
			}
			fi, err := root.Lstat(rel)
			if err != nil {
				root.Close()
				return err
			}
			if !fi.Mode().IsRegular() || fi.Size() != entry.entry.Size || fi.ModTime().Unix() != entry.entry.MTime {
				root.Close()
				return fmt.Errorf("file changed before hashing: %s", rel)
			}
			f, err := openHash(root, rel)
			if err != nil {
				root.Close()
				return err
			}
			opened, err := f.Stat()
			if err != nil || !os.SameFile(fi, opened) {
				f.Close()
				root.Close()
				return fmt.Errorf("file identity changed: %s", rel)
			}
			h := sha256.New()
			buf := make([]byte, 1<<20)
			p.Current = rel
			for {
				if err = ctx.Err(); err != nil {
					break
				}
				var n int
				n, err = f.Read(buf)
				if n > 0 {
					h.Write(buf[:n])
					p.Bytes += int64(n)
					if progress != nil {
						progress(p)
					}
				}
				if err != nil {
					break
				}
			}
			end, statErr := f.Stat()
			pathInfo, pathErr := root.Lstat(rel)
			f.Close()
			root.Close()
			if err != nil && !errors.Is(err, io.EOF) {
				return err
			}
			if statErr != nil || pathErr != nil || !os.SameFile(fi, pathInfo) || end.Size() != fi.Size() || !end.ModTime().Equal(fi.ModTime()) {
				return fmt.Errorf("file changed during hashing: %s", rel)
			}
			out = append(out, Fingerprint{RootPath: []byte(entry.root.Path), Root: entry.root.ID, Path: []byte(rel), Size: fi.Size(), Modified: fi.ModTime().UnixNano(), Hash: hex.EncodeToString(h.Sum(nil))})
			p.Files++
			if progress != nil {
				progress(p)
			}
		}
	}
	// Results from other roots are retained when a scoped run completes.
	previous, err := c.fingerprints()
	if err != nil {
		return err
	}
	ids := map[string]bool{}
	for _, root := range roots {
		ids[root.ID] = true
	}
	for _, f := range previous {
		if !ids[f.Root] {
			out = append(out, f)
		}
	}
	b, err := json.Marshal(out)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(c.dir, ".hashes-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	return os.Rename(name, filepath.Join(c.dir, "hashes.json"))
}
func (c *Catalog) fingerprints() ([]Fingerprint, error) {
	b, err := os.ReadFile(filepath.Join(c.dir, "hashes.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Fingerprint
	err = json.Unmarshal(b, &out)
	return out, err
}

// DuplicateFiles filters cached hashes against both the current tree and disk.
func (c *Catalog) DuplicateFiles(ctx context.Context, roots []Root, minSize int64) ([][]Fingerprint, error) {
	c.hashMu.Lock()
	defer c.hashMu.Unlock()
	stored, err := c.fingerprints()
	if err != nil {
		return nil, err
	}
	byRoot := map[string]map[string]Fingerprint{}
	for _, f := range stored {
		if f.Size < minSize {
			continue
		}
		if byRoot[f.Root] == nil {
			byRoot[f.Root] = map[string]Fingerprint{}
		}
		byRoot[f.Root][string(f.Path)] = f
	}
	groups := map[string][]Fingerprint{}
	for _, root := range roots {
		result, err := c.Search(ctx, root, index.Query{Hidden: true, Category: "file", MinSize: minSize})
		if err != nil {
			return nil, err
		}
		handle, err := os.OpenRoot(root.Path)
		if err != nil {
			return nil, err
		}
		err = result.Each(ctx, func(e index.Entry) error {
			if e.Link {
				return nil
			}
			rel, err := filepath.Rel(root.Path, e.Path)
			if err != nil {
				return err
			}
			f, ok := byRoot[root.ID][rel]
			if !ok || f.Size != e.Size || string(f.RootPath) != root.Path {
				return nil
			}
			fi, err := handle.Lstat(rel)
			if err != nil || !fi.Mode().IsRegular() || fi.Size() != f.Size || fi.ModTime().UnixNano() != f.Modified {
				return nil
			}
			key := fmt.Sprintf("%d/%s", f.Size, f.Hash)
			groups[key] = append(groups[key], f)
			return nil
		})
		handle.Close()
		if err != nil {
			return nil, err
		}
	}
	out := [][]Fingerprint{}
	for _, g := range groups {
		if len(g) > 1 {
			slices.SortFunc(g, func(a, b Fingerprint) int {
				if a.Root < b.Root {
					return -1
				}
				if a.Root > b.Root {
					return 1
				}
				return slices.Compare(a.Path, b.Path)
			})
			out = append(out, g)
		}
	}
	slices.SortFunc(out, func(a, b []Fingerprint) int {
		aw, bw := a[0].Size*int64(len(a)-1), b[0].Size*int64(len(b)-1)
		if aw > bw {
			return -1
		}
		if aw < bw {
			return 1
		}
		if a[0].Hash < b[0].Hash {
			return -1
		}
		if a[0].Hash > b[0].Hash {
			return 1
		}
		return 0
	})
	return out, nil
}
