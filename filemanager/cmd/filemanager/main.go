package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"

	"filemanager/api"
	"filemanager/service"
	"filemanager/sharing"
)

func main() {
	// This budget covers Go-managed memory, not resident mmap/file-cache pages.
	if os.Getenv("GOMEMLIMIT") == "" {
		debug.SetMemoryLimit(512 << 20)
	}
	share := flag.String("share", "", "read-only peer sharing configuration")
	socket := flag.String("socket", "", "private Unix socket path")
	root := flag.String("root", "", "filesystem root exposed as root id local")
	journal := flag.String("journal", "", "durable mutation journal; omit for read-only service")
	owner := flag.Bool("owner-stdin", false, "stop when the owning frontend closes stdin")
	flag.Parse()
	if *share == "" && (*socket == "" || *root == "") {
		fmt.Fprintln(os.Stderr, "--socket and --root are required")
		os.Exit(2)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if *owner {
		go func() { io.Copy(io.Discard, os.Stdin); cancel() }()
	}
	if *share != "" {
		if err := sharing.Run(ctx, *share, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if err := service.RunLocal(ctx, *socket, []api.Root{{ID: "local", Name: "本机", Path: *root}}, os.Stdout, *journal); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
