package config

import (
	"os"
	"path/filepath"
	"testing"
)

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
