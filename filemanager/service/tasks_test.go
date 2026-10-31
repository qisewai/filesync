package service

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filemanager/api"
	"filemanager/client"
)

func taskService(t *testing.T, log string) (*Service, *client.Client, func()) {
	t.Helper()
	s, err := New([]api.Root{{ID: "local", Path: "/"}})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.EnableWrites(log); err != nil {
		t.Fatal(err)
	}
	if err = s.EnableLocalTransfers(); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(s.Handler())
	return s, client.New(server.URL, server.Client()), func() { server.Close(); s.Close() }
}
func taskLocation(p string) api.Location {
	return api.Location{Root: "local", Path: strings.TrimPrefix(p, "/")}
}
func waitTask(t *testing.T, c *client.Client, id string) api.Task {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		s, err := c.Task(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if s.Finished() {
			return s
		}
		time.Sleep(time.Millisecond)
	}
}
func TestTransferTasksPersistAndDeduplicate(t *testing.T) {
	log := filepath.Join(t.TempDir(), "journal")
	_, c, closeService := taskService(t, log)
	root := t.TempDir()
	from, to := filepath.Join(root, "from"), filepath.Join(root, "to")
	os.Mkdir(from, 0700)
	os.WriteFile(filepath.Join(from, "file"), []byte("content"), 0600)
	q := api.TransferRequest{ID: strings.Repeat("a", 32), Source: taskLocation(from), Target: taskLocation(to), FileConflict: "fail", DirectoryConflict: "fail"}
	if _, err := c.SubmitTransfer(context.Background(), q); err != nil {
		t.Fatal(err)
	}
	state := waitTask(t, c, q.ID)
	if state.State != "done" || state.Done != 7 || state.Total != 7 {
		t.Fatalf("%+v", state)
	}
	os.WriteFile(filepath.Join(to, "file"), []byte("changed"), 0600)
	if _, err := c.SubmitTransfer(context.Background(), q); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(to, "file")); string(b) != "changed" {
		t.Fatal("retry repeated file mutation")
	}
	closeService()
	_, c, closeService = taskService(t, log)
	defer closeService()
	if state := waitTask(t, c, q.ID); state.State != "done" {
		t.Fatalf("restored %+v", state)
	}
	if _, err := c.SubmitTransfer(context.Background(), q); err != nil {
		t.Fatal(err)
	}
	q.Move = true
	if _, err := c.SubmitTransfer(context.Background(), q); err == nil {
		t.Fatal("reused id for different request")
	}
}
func TestCancelQueuedTransferAndProtectJournal(t *testing.T) {
	log := filepath.Join(t.TempDir(), "journal")
	s, c, closeService := taskService(t, log)
	defer closeService()
	root := t.TempDir()
	from, to := filepath.Join(root, "from"), filepath.Join(root, "to")
	os.WriteFile(from, []byte("source"), 0600)
	s.writer.gate <- struct{}{}
	q := api.TransferRequest{ID: strings.Repeat("b", 32), Source: taskLocation(from), Target: taskLocation(to), Move: true, FileConflict: "fail", DirectoryConflict: "fail"}
	if _, err := c.SubmitTransfer(context.Background(), q); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CancelTask(context.Background(), q.ID); err != nil {
		t.Fatal(err)
	}
	state := waitTask(t, c, q.ID)
	<-s.writer.gate
	if state.State != "canceled" {
		t.Fatalf("%+v", state)
	}
	if _, err := os.Stat(from); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(to); !os.IsNotExist(err) {
		t.Fatal("canceled task changed destination")
	}
	q.ID = strings.Repeat("c", 32)
	q.Target = taskLocation(log)
	q.FileConflict = "replace"
	if _, err := c.SubmitTransfer(context.Background(), q); err == nil {
		t.Fatal("allowed overwriting journal")
	}
	q.Target = taskLocation(to)
	q.Protected = []api.Location{taskLocation(from)}
	if _, err := c.SubmitTransfer(context.Background(), q); err != nil {
		t.Fatal(err)
	}
	if state := waitTask(t, c, q.ID); state.State != "failed" {
		t.Fatalf("protected source %+v", state)
	}
}
func TestStopTasksCancelsAndInterruptedDoesNotResume(t *testing.T) {
	log := filepath.Join(t.TempDir(), "journal")
	s, c, closeService := taskService(t, log)
	root := t.TempDir()
	from, to := filepath.Join(root, "from"), filepath.Join(root, "to")
	os.WriteFile(from, []byte("source"), 0600)
	s.writer.gate <- struct{}{}
	q := api.TransferRequest{ID: strings.Repeat("d", 32), Source: taskLocation(from), Target: taskLocation(to), Move: true, FileConflict: "fail", DirectoryConflict: "fail"}
	if _, err := c.SubmitTransfer(context.Background(), q); err != nil {
		t.Fatal(err)
	}
	s.StopTasks()
	<-s.writer.gate
	if state := waitTask(t, c, q.ID); state.State != "canceled" {
		t.Fatalf("%+v", state)
	}
	q.ID = strings.Repeat("e", 32)
	b, _ := json.Marshal(taskRecord{Phase: "task_begin", Request: &q})
	s.writer.log.Append(b)
	closeService()
	_, c, closeService = taskService(t, log)
	defer closeService()
	if state := waitTask(t, c, q.ID); state.State != "interrupted" {
		t.Fatalf("%+v", state)
	}
	if _, err := c.SubmitTransfer(context.Background(), q); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(from); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(to); !os.IsNotExist(err) {
		t.Fatal("interrupted task resumed")
	}
}
