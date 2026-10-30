package localindex

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// An actual format-2 payload verifies migration independently of the new
// writer: old directory paths must still address the original file rows.
func TestLoadLegacyDirectoryPaths(t *testing.T) {
	root := t.TempDir()
	opts := ScanOptions{Roots: []string{root}}
	var payload bytes.Buffer
	w := snapshotWriter{ctx: context.Background(), w: &payload}
	w.str(root)
	w.strings([]string{root, filepath.Join(root, "文档")})
	w.strings(nil)
	w.strings([]string{"", "txt"})
	w.array([]int64{0, 0})
	w.raw([]byte{0, 0})
	w.u32(1)
	w.u32(1)
	w.blob([]byte("报告.txt\x00"))
	w.blob([]byte("报告.txt\x00"))
	w.array([]uint32{0})
	w.array([]uint32{0})
	w.array([]uint32{1})
	w.array([]int64{7})
	w.array([]int64{42})
	w.array([]uint16{1})
	w.raw([]byte{0})
	w.raw([]byte{category("txt", false)})
	if w.err != nil {
		t.Fatal(w.err)
	}
	header := make([]byte, snapshotHeaderSize)
	copy(header, snapshotMagic[:])
	binary.LittleEndian.PutUint32(header[8:], 2)
	binary.LittleEndian.PutUint64(header[16:], uint64(payload.Len()))
	binary.LittleEndian.PutUint32(header[36:], 1)
	scope, err := scopeDigest(opts)
	if err != nil {
		t.Fatal(err)
	}
	copy(header[40:72], scope[:])
	digest := sha256.New()
	digest.Write(payload.Bytes())
	digest.Write(header[:72])
	copy(header[72:], digest.Sum(nil))
	file := filepath.Join(t.TempDir(), "legacy.bin")
	if err := os.WriteFile(file, append(header, payload.Bytes()...), 0o600); err != nil {
		t.Fatal(err)
	}
	x := New()
	if _, err := x.LoadSnapshot(context.Background(), file, opts, nil); err != nil {
		t.Fatal(err)
	}
	got := page(t, search(t, x, Query{Text: "报告"}), 0, 10)
	if len(got) != 1 || got[0].Path != filepath.Join(root, "文档", "报告.txt") || got[0].Size != 7 {
		t.Fatalf("migrated entries: %+v", got)
	}
}

func TestDirectoryTreePreservesIDsAndMissingAncestors(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	child := filepath.Join(root, "parent", "child")
	r := &rootData{path: root, dirs: []string{root, child}, dirStamp: []int64{1, 2}, dirReady: []byte{1, 1}}
	names, parents, stamps, ready, err := directoryTree(r)
	if err != nil {
		t.Fatal(err)
	}
	paths, err := directoryPaths(root, names, parents)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(paths, []string{root, child, filepath.Dir(child)}) || parents[1] != 2 || stamps[1] != 2 || ready[2] != 0 {
		t.Fatalf("paths=%v parents=%v stamps=%v ready=%v", paths, parents, stamps, ready)
	}
}

func TestDirectoryTreeRejectsInvalidParents(t *testing.T) {
	root := t.TempDir()
	for _, tc := range []struct {
		names   []string
		parents []uint32
	}{
		{[]string{"", "a"}, []uint32{0, 2}},
		{[]string{"", "a", "b"}, []uint32{0, 2, 1}},
		{[]string{"", ".."}, []uint32{0, 0}},
		{[]string{"", "a", "a"}, []uint32{0, 0, 0}},
	} {
		if _, err := directoryPaths(root, tc.names, tc.parents); !errors.Is(err, ErrSnapshotFormat) {
			t.Errorf("names=%v parents=%v: %v", tc.names, tc.parents, err)
		}
	}
}
