package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestLocalIndexSettingsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := &Store{Dir: dir, LocalIndex: &LocalIndexSettings{Roots: []string{dir, "/home/example/文档"}, Excludes: []string{".cache/", "*.tmp"}}}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	again, err := Load(dir)
	if err != nil || !reflect.DeepEqual(again.LocalIndex, s.LocalIndex) {
		t.Fatalf("settings %+v, %v", again, err)
	}
	// An explicitly empty scope is different from missing settings (defaults).
	s.LocalIndex = &LocalIndexSettings{}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	again, err = Load(dir)
	if err != nil || again.LocalIndex == nil || len(again.LocalIndex.Roots) != 0 {
		t.Fatalf("empty %+v, %v", again, err)
	}
}

// The logins of version 0.1 become servers, and its tasks to local
// directories go.
func TestLoadVersion01(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "tasks.json"), []byte(`{
		"tasks": [{"id": "a", "name": "本地", "source": "/x", "target": "/y"}],
		"sessions": {"192.168.0.235:8090": "tok"},
		"lastServer": "192.168.0.235:8090"
	}`), 0o600)
	s, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Servers) != 1 || s.Servers[0].Addr != "192.168.0.235:8090" || s.Servers[0].Token != "tok" || s.Servers[0].ID == "" {
		t.Errorf("servers %+v", s.Servers)
	}
	if len(s.Tasks) != 0 {
		t.Errorf("tasks %+v", s.Tasks)
	}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	again, err := Load(dir)
	if err != nil || len(again.Servers) != 1 || again.Servers[0].ID != s.Servers[0].ID {
		t.Errorf("after saving: %+v, %v", again, err)
	}
	if info, _ := os.Stat(filepath.Join(dir, "tasks.json")); info.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", info.Mode())
	}
}

func TestServers(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	a, b := &Server{Name: "家里", Addr: "h:1"}, &Server{Addr: "h:2"}
	s.AddServer(a)
	s.AddServer(b)
	s.Add(&Task{Name: "t", Server: a.ID})
	if s.ServerByAddr("h:2") != b || s.Server(a.ID) != a || b.Title() != "h:2" || a.Title() != "家里" {
		t.Error("lookups")
	}
	if err := s.RemoveServer(a.ID); err == nil {
		t.Error("removed a server in use")
	}
	if err := s.RemoveServer(b.ID); err != nil || len(s.Servers) != 1 {
		t.Errorf("remove: %v, %d left", err, len(s.Servers))
	}
}

func TestBackupPath(t *testing.T) {
	for _, c := range []struct {
		source, want string
		withDir      bool
	}{
		{"/home/ning/work/xiaomai", "备份/xiaomai", true},
		{"/home/ning/work/xiaomai/", "备份/xiaomai", true},
		{`C:\Users\ning\文档`, "备份/文档", true},
		{"/home/ning/work/xiaomai", "备份", false},
		{"/", "备份", true},
		{`C:\`, "备份", true},
		{"", "备份", true},
	} {
		if got := BackupPath("备份", c.source, c.withDir); got != c.want {
			t.Errorf("%q, %v: %q, want %q", c.source, c.withDir, got, c.want)
		}
	}
	if got := BackupPath("", "/x/y", true); got != "y" {
		t.Errorf("into a root: %q", got)
	}
}

func TestContents(t *testing.T) {
	task := &Task{Source: "/home/me/文档"}
	if task.Contents() != "/home/me/文档" || task.Named() != "/home/me/文档" {
		t.Errorf("%q %q", task.Contents(), task.Named())
	}
	task.Items = []string{"报告.pdf"}
	if task.Contents() != "/home/me/文档 中的 报告.pdf" || ArchiveName(task.Named(), time.Time{}, "") != "报告.pdf-00010101-000000.tar" {
		t.Errorf("%q %q", task.Contents(), task.Named())
	}
	task.Items = []string{"a", "b", "c", "d"}
	if task.Contents() != "/home/me/文档 中的 a、b、c 等 4 项" {
		t.Errorf("%q", task.Contents())
	}
	// Entries go straight into the target.
	task.WithDir, task.Path = true, "备份"
	if task.BackupPath() != "备份" {
		t.Errorf("%q", task.BackupPath())
	}
}
