package localpath

import "testing"

func TestWindowsLocation(t *testing.T) {
	for _, tc := range []struct{ path, root, relative string }{
		{`C:\`, "local-C:", ""},
		{`c:\Users\测试\hello.txt`, "local-C:", "Users/测试/hello.txt"},
		{`D:/资料/文件.txt`, "local-D:", "资料/文件.txt"},
	} {
		got := Location(tc.path)
		if got.Root != tc.root || got.Path != tc.relative {
			t.Fatalf("%q => %+v", tc.path, got)
		}
	}
}
