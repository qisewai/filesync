package places

import (
	"context"
	"os"
	"testing"
)

func TestURLs(t *testing.T) {
	for in, want := range map[string]bool{
		"smb://host/share": true, "SFTP://h/x": true, "/home/ning": false, `C:\data`: false,
		"file:///home": false, "smb://": false, `\\server\share`: false,
	} {
		if IsURL(in) != want {
			t.Errorf("IsURL(%q) = %v", in, !want)
		}
	}
	readable := "smb://wangning@192.168.0.235/王宁/照片 #1/100%"
	enc := encodeURL(readable)
	if enc != "smb://wangning@192.168.0.235/%E7%8E%8B%E5%AE%81/%E7%85%A7%E7%89%87%20%231/100%25" {
		t.Errorf("encoded %s", enc)
	}
	if decodeURL(enc) != readable {
		t.Errorf("decoded %s", decodeURL(enc))
	}
}

// TestDesktop prints what this machine has: FILESYNC_PLACES=smb://… go test -v ./places
func TestDesktop(t *testing.T) {
	u := os.Getenv("FILESYNC_PLACES")
	if u == "" {
		t.Skip("set FILESYNC_PLACES to a URL to try")
	}
	dir, err := Resolve(context.Background(), u)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("resolved %s → %s", u, dir)
	t.Logf("stable %s → %s", dir+"/sub", Stable(context.Background(), dir+"/sub"))
	entries, err := os.ReadDir(dir)
	t.Logf("%d entries, %v", len(entries), err)
}
