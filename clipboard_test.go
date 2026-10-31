package main

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"filemanager/remoteclient/fitest"
	mt "github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"
)

func TestSystemFileClipboardFormats(t *testing.T) {
	paths := []string{filepath.Join(t.TempDir(), "中文 # 100%.txt"), filepath.Join(t.TempDir(), "line\nfile")}
	for _, cut := range []bool{false, true} {
		data, err := systemFiles(paths, cut)
		if err != nil {
			t.Fatal(err)
		}
		for _, format := range []mt.Format{mt.FileList, mt.URIList, gnomeFiles, kdeCut, dropEffect} {
			b, err := data.Read(format)
			if err != nil {
				t.Fatal(format, err)
			}
			if format == dropEffect {
				want := uint32(1)
				if cut {
					want = 2
				}
				if binary.LittleEndian.Uint32(b) != want {
					t.Fatal("wrong native drop effect")
				}
				continue
			}
			if format == kdeCut {
				continue
			}
			reps := []mt.Representation{mt.Bytes(format, b)}
			if format != gnomeFiles {
				flag, _ := data.Read(kdeCut)
				reps = append(reps, mt.Bytes(kdeCut, flag))
			}
			cl := decodeSystemClip(mt.New(mt.NewItem(reps...)))
			if cl == nil || cl.move != cut {
				t.Fatalf("%s did not decode cut=%v: %+v", format, cut, cl)
			}
			var got []string
			for _, e := range cl.es {
				got = append(got, e.Path)
			}
			if !reflect.DeepEqual(got, paths) {
				t.Fatalf("roundtrip %q want %q", got, paths)
			}
		}
	}
	if _, err := systemFiles([]string{"relative.txt"}, false); err == nil {
		t.Fatal("relative path accepted")
	}
	data := mt.New(mt.NewItem(mt.Bytes(mt.URIList, []byte("https://example.test/a\r\nfile://remote/tmp/a\r\nfile:///tmp/a\r\nfile:///tmp/a"))))
	cl := decodeSystemClip(data)
	if cl == nil || len(cl.es) != 1 || cl.es[0].Path != "/tmp/a" {
		t.Fatal("unsafe or duplicate paths", cl)
	}
}

func TestClipboardExternalReplacementInvalidatesInternal(t *testing.T) {
	dir := t.TempDir()
	localWrite(t, dir, "one.txt", 1)
	h := localSearchHarness(t, dir)
	h.clickPage("one.txt")
	h.tt.Key(ui.Ctrl, ui.KeyX)
	h.settle()
	if h.a.clip == nil {
		t.Fatal("copy absent")
	}
	h.tt.SetClipboard("unrelated text")
	h.tt.Key(ui.Ctrl, ui.KeyV)
	h.settle()
	if h.a.clip != nil || len(h.a.transfers) != 0 {
		t.Fatal("stale files pasted after external text")
	}
	// The same path copied by another app must replace our earlier cut mode.
	h.clickPage("one.txt")
	h.tt.Key(ui.Ctrl, ui.KeyX)
	h.settle()
	data, _ := systemFiles([]string{filepath.Join(dir, "one.txt")}, false)
	h.tt.SetClipboardData(data)
	dst := t.TempDir()
	h.a.openLocal(dst)
	h.settle()
	h.tt.Key(ui.Ctrl, ui.KeyV)
	h.waitStarted(1)
	h.waitTransfers()
	if _, err := os.Stat(filepath.Join(dir, "one.txt")); err != nil {
		t.Fatal("external copy wrongly moved source", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "one.txt")); err != nil {
		t.Fatal(err)
	}
}

func TestExternalCutClearsOnlyAfterSuccess(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	localWrite(t, src, "move.txt", 1)
	h := localSearchHarness(t, dst)
	data, _ := systemFiles([]string{filepath.Join(src, "move.txt")}, true)
	gnome, _ := data.Read(gnomeFiles)
	h.tt.SetClipboardData(mt.New(mt.NewItem(mt.Bytes(gnomeFiles, gnome))))
	h.tt.Key(ui.Ctrl, ui.KeyV)
	h.waitStarted(1)
	h.waitTransfers()
	if _, err := os.Stat(filepath.Join(dst, "move.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(src, "move.txt")); !os.IsNotExist(err) {
		t.Fatal("source not moved", err)
	}
	if len(h.tt.ClipboardData().Formats()) != 0 || h.a.clip != nil {
		t.Fatal("completed cut still offered")
	}
}

func TestCutConflictCancelPreservesClipboard(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	localWrite(t, src, "same.txt", 1)
	localWrite(t, dst, "same.txt", 2)
	h := localSearchHarness(t, src)
	h.clickPage("same.txt")
	h.tt.Key(ui.Ctrl, ui.KeyX)
	h.settle()
	h.a.openLocal(dst)
	h.settle()
	h.tt.Key(ui.Ctrl, ui.KeyV)
	h.settle()
	h.want("合并同名目录并覆盖文件？")
	h.click("取消")
	if h.a.clip == nil || !h.a.clip.move || len(h.tt.ClipboardData().Formats()) == 0 {
		t.Fatal("cancel lost cut")
	}
	if _, err := os.Stat(filepath.Join(src, "same.txt")); err != nil {
		t.Fatal(err)
	}
}

func TestCutFailurePreservesClipboard(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	localWrite(t, src, "missing.txt", 1)
	h := localSearchHarness(t, src)
	h.clickPage("missing.txt")
	h.tt.Key(ui.Ctrl, ui.KeyX)
	h.settle()
	if err := os.Remove(filepath.Join(src, "missing.txt")); err != nil {
		t.Fatal(err)
	}
	h.a.openLocal(dst)
	h.settle()
	h.tt.Key(ui.Ctrl, ui.KeyV)
	h.waitStarted(1)
	deadline := time.After(10 * time.Second)
	for h.a.activeTransfers() > 0 {
		select {
		case fn := <-h.queue:
			fn()
			h.tt.Frame()
		case <-deadline:
			t.Fatal("failed transfer did not finish")
		}
	}
	h.settle()
	if h.a.clip == nil || h.a.clip.busy || len(h.tt.ClipboardData().Formats()) == 0 {
		t.Fatal("failed move lost clipboard")
	}
}

func TestExternalCutUploadClearsAfterSuccess(t *testing.T) {
	f := fitest.New(t)
	h := browserHarness(t, f)
	src := t.TempDir()
	localWrite(t, src, "upload.txt", 7)
	data, err := systemFiles([]string{filepath.Join(src, "upload.txt")}, true)
	if err != nil {
		t.Fatal(err)
	}
	h.tt.SetClipboardData(data)
	h.tt.Key(ui.Ctrl, ui.KeyV)
	h.waitStarted(1)
	h.waitTransfers()
	if b, err := os.ReadFile(filepath.Join(f.Root, "upload.txt")); err != nil || len(b) != 7 {
		t.Fatalf("upload %q %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(src, "upload.txt")); !os.IsNotExist(err) {
		t.Fatal("source not removed", err)
	}
	if h.a.clip != nil || len(h.tt.ClipboardData().Formats()) != 0 {
		t.Fatal("completed cut remains in clipboard")
	}
}
