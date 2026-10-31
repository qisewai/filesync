package localprocess_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"filemanager/api"
	"filemanager/localpath"
	"filemanager/localprocess"
	fileindex "filemanager/remoteclient"
	"filemanager/sharing"
)

func TestRealBackendLifecycle(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "windows" {
		t.Skip("Linux/Windows IPC runtime acceptance")
	}
	dir := t.TempDir()
	exe := filepath.Join(dir, "filemanager")
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	cmd := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-o", exe, "../cmd/filemanager")
	cmd.Env = append(os.Environ(), "GOTOOLCHAIN=local", "GOPROXY=off")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build backend: %v\n%s", err, b)
	}
	// Short private path keeps the Unix socket below the platform length limit.
	run, err := os.MkdirTemp("", "fm-runtime-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(run)
	t.Setenv("XDG_RUNTIME_DIR", run)
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "hello"), []byte("world"), 0600)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	journal := filepath.Join(t.TempDir(), "operations.log")
	p, err := localprocess.StartWritable(ctx, exe, root, journal)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	es, err := p.Client.List(ctx, api.ListRequest{Location: api.Location{Root: "local"}})
	if err != nil || len(es) != 1 || es[0].Name != "hello" {
		t.Fatalf("real IPC: %+v %v", es, err)
	}
	if _, err := p.Client.Mutate(ctx, api.MutationRequest{Action: "mkdir", Target: api.Location{Root: "local", Path: "created"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "created")); err != nil {
		t.Fatal(err)
	}
	if other, err := localprocess.StartWritable(ctx, exe, root, journal); err == nil {
		other.Close()
		t.Fatal("second process acquired same journal")
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Client.Info(ctx); err == nil {
		t.Fatal("backend still responds after owner closed")
	}
	if st, err := os.Stat(journal); err != nil || st.Size() <= 40 {
		t.Fatalf("durable journal missing: %v %v", st, err)
	}
	resumed, err := localprocess.StartWritable(ctx, exe, root, journal)
	if err != nil {
		t.Fatal(err)
	}
	if err := resumed.Close(); err != nil {
		t.Fatal(err)
	}
	desktop, err := localprocess.StartWritable(ctx, exe, "/", journal)
	if err != nil {
		t.Fatal(err)
	}
	defer desktop.Close()
	info, err := desktop.Client.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, capability := range []string{"local_backup", "local_index", "local_transfer_tasks", "trash"} {
		if !slices.Contains(info.Capabilities, capability) {
			t.Fatalf("missing desktop capability %s: %+v", capability, info)
		}
	}

	location := localpath.Location
	task, err := desktop.Client.SubmitTransfer(ctx, api.TransferRequest{ID: strings.Repeat("a", 32), Source: location(filepath.Join(root, "hello")), Target: location(filepath.Join(root, "copy")), FileConflict: "fail", DirectoryConflict: "fail"})
	if err != nil {
		t.Fatal(err)
	}
	for !task.Finished() {
		task, err = desktop.Client.Task(ctx, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond)
	}
	if task.State != "done" {
		t.Fatalf("IPC transfer: %+v", task)
	}
	if b, err := os.ReadFile(filepath.Join(root, "copy")); err != nil || string(b) != "world" {
		t.Fatalf("copy %q %v", b, err)
	}
	if err := desktop.Close(); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(run)
	if err != nil || len(entries) != 0 {
		t.Fatalf("runtime leak: %v %v", entries, err)
	}
}

func TestRealSharingBackendLifecycle(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "filemanager")
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	cmd := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-o", exe, "../cmd/filemanager")
	cmd.Env = append(os.Environ(), "GOTOOLCHAIN=local", "GOPROXY=off")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, b)
	}
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "hello.txt"), []byte("peer contents"), 0600)
	cfg, err := sharing.NewConfig()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Listen = "127.0.0.1:0"
	cfg.Roots = []api.Root{{ID: "1", Name: "disk", Path: root}}
	configPath := filepath.Join(t.TempDir(), "sharing.json")
	if err := sharing.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	p, err := localprocess.StartShare(ctx, exe, configPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	invitation, err := cfg.Invitation(p.Ready.Address)
	if err != nil {
		t.Fatal(err)
	}
	_, address, key, err := sharing.ParseInvitation(invitation)
	if err != nil {
		t.Fatal(err)
	}
	client := fileindex.New(address, "")
	if err := client.Login(ctx, key); err != nil {
		t.Fatal(err)
	}
	entries, err := client.List(ctx, 1, "")
	if err != nil || len(entries) != 1 {
		t.Fatalf("list %+v %v", entries, err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Roots(ctx); err == nil {
		t.Fatal("sharing responds after owner closes")
	}
}
