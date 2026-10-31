package sharing_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filemanager/api"
	fileindex "filemanager/remoteclient"
	"filemanager/sharing"
)

func start(t *testing.T, c sharing.Config, configPath string) (sharing.Ready, context.CancelFunc, <-chan error) {
	t.Helper()
	if err := sharing.Save(configPath, c); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() { err := sharing.Run(ctx, configPath, pw); pw.CloseWithError(err); done <- err }()
	var ready sharing.Ready
	if err := json.NewDecoder(pr).Decode(&ready); err != nil {
		cancel()
		t.Fatal(err)
	}
	pr.Close()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(10 * time.Second):
			t.Error("sharing did not stop")
		}
	})
	return ready, cancel, done
}
func TestPeerReadOnlyEndToEnd(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "资料", "空目录"), 0700)
	content := bytes.Repeat([]byte("hello 中文\n"), 1000)
	os.WriteFile(filepath.Join(root, "资料", "one.txt"), content, 0600)
	cfg, err := sharing.NewConfig()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Listen = "127.0.0.1:0"
	cfg.Roots = []api.Root{{ID: "1", Name: "共享磁盘", Path: root}}
	configPath := filepath.Join(root, "private", "sharing.json")
	ready, _, _ := start(t, cfg, configPath)
	invitation, err := cfg.Invitation(ready.Address)
	if err != nil {
		t.Fatal(err)
	}
	_, address, key, err := sharing.ParseInvitation(invitation)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client := fileindex.New(address, "")
	if _, err := client.Roots(ctx); !errors.Is(err, fileindex.ErrAuth) {
		t.Fatal("unauthenticated access", err)
	}
	if err := client.Login(ctx, "incorrect"); err == nil {
		t.Fatal("accepted bad key")
	}
	if err := client.Login(ctx, key); err != nil {
		t.Fatal(err)
	}
	roots, err := client.Roots(ctx)
	if err != nil || len(roots) != 1 || !roots[0].ReadOnly || roots[0].Path != "" {
		t.Fatalf("roots %+v %v", roots, err)
	}
	entries, err := client.List(ctx, 1, "")
	if err != nil || len(entries) != 1 || entries[0].Name != "资料" {
		t.Fatalf("list %+v %v", entries, err)
	}
	dirs, err := client.Dirs(ctx, 1, "资料")
	if err != nil || len(dirs) != 1 || dirs[0] != "空目录" {
		t.Fatalf("dirs %v %v", dirs, err)
	}
	props, err := client.Props(ctx, 1, "资料/one.txt")
	if err != nil || props.Size != int64(len(content)) || !props.ReadOnly {
		t.Fatalf("props %+v %v", props, err)
	}
	dest := filepath.Join(t.TempDir(), "download.txt")
	if err := client.DownloadFileResumable(ctx, 1, "资料/one.txt", dest, filepath.Join(filepath.Dir(dest), ".filesync-download-test.part"), nil); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dest)
	if err != nil || !bytes.Equal(got, content) {
		t.Fatal("download mismatch", err)
	}
	folder := t.TempDir()
	es, err := client.Download(ctx, 1, []string{"资料"}, folder, nil)
	if err != nil || len(es) != 0 {
		t.Fatal("directory download", es, err)
	}
	got, err = os.ReadFile(filepath.Join(folder, "资料", "one.txt"))
	if err != nil || !bytes.Equal(got, content) {
		t.Fatal("directory contents", err)
	}
	if st, err := os.Stat(filepath.Join(folder, "资料", "空目录")); err != nil || !st.IsDir() {
		t.Fatal("lost empty directory", err)
	}
	for _, p := range []string{"../outside", "private/sharing.json", "/etc/passwd"} {
		if err := client.DownloadFile(ctx, 1, p, filepath.Join(t.TempDir(), "bad"), nil); err == nil {
			t.Fatal("unsafe read", p)
		}
	}
	if err := os.Symlink(filepath.Dir(configPath), filepath.Join(root, "alias")); err != nil {
		t.Fatal(err)
	}
	if _, err := client.List(ctx, 1, "alias"); err == nil {
		t.Fatal("read protected symlink")
	}
	if err := client.DownloadFile(ctx, 1, "alias/sharing.json", filepath.Join(t.TempDir(), "bad"), nil); err == nil {
		t.Fatal("downloaded config alias")
	}

	if err := os.Symlink("one.txt", filepath.Join(root, "资料", "link.txt")); err != nil {
		t.Fatal(err)
	}
	if es, err := client.Download(ctx, 1, []string{"资料"}, t.TempDir(), nil); err == nil && len(es) == 0 {
		t.Fatal("accepted incomplete directory export")
	}
	if _, err := client.Download(ctx, 1, []string{"资料/link.txt"}, t.TempDir(), nil); err == nil {
		t.Fatal("exported top-level symlink")
	}
	if err := client.Mkdir(ctx, 1, "", "must-not-exist"); err == nil {
		t.Fatal("peer allowed writes")
	}
	if _, err := os.Stat(filepath.Join(root, "must-not-exist")); !os.IsNotExist(err) {
		t.Fatal("peer mutated root")
	}
	// Pin mismatch must fail even if the session token and address are correct.
	badAddress, err := fileindex.PeerAddress(ready.Address, strings.Repeat("0", 64))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fileindex.New(badAddress, client.Token).Roots(ctx); err == nil {
		t.Fatal("accepted wrong certificate")
	}
	// No trusted-local endpoint can be reached, even after authenticating.
	tr := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	defer tr.CloseIdleConnections()
	hc := &http.Client{Transport: tr, Timeout: 5 * time.Second}
	for _, route := range []string{"/v1/index", "/v1/remote", "/v1/roots", "/v1/mutate"} {
		req, _ := http.NewRequest("POST", "https://"+ready.Address+route, strings.NewReader(`{}`))
		req.Header.Set("X-Requested-With", "fileindex")
		req.AddCookie(&http.Cookie{Name: "fi_session", Value: client.Token})
		res, err := hc.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode/100 == 2 {
			t.Fatal("trusted endpoint exposed", route)
		}
	}
	req, _ := http.NewRequest("GET", "https://"+ready.Address+"/api/roots", nil)
	req.Header.Set("Origin", "https://example.com")
	req.Header.Set("X-Requested-With", "fileindex")
	req.AddCookie(&http.Cookie{Name: "fi_session", Value: client.Token})
	res, err := hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 403 {
		t.Fatal("accepted browser origin")
	}
	if err := client.Logout(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Roots(ctx); !errors.Is(err, fileindex.ErrAuth) {
		t.Fatal("logout failed", err)
	}
}
func TestInvitationValidation(t *testing.T) {
	for _, text := range []string{"http://localhost", "mygo://localhost:1?key=short", "mygo://user@localhost:1?key=" + strings.Repeat("x", 32), "mygo://localhost:1/path?key=" + strings.Repeat("x", 32)} {
		if _, _, _, err := sharing.ParseInvitation(text); err == nil {
			t.Fatal("accepted", text)
		}
	}
}
