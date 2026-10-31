package index

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// Operation records local filesystem intent independently of snapshot
// generations. A start sequence identifies the matching completion. Failed
// and unfinished operations may have partially changed the disk and require
// reconciliation, not blind replay.
type Operation struct {
	Version     int                  `json:"v"`
	Phase       string               `json:"phase"`
	ID          uint64               `json:"id,omitempty"`
	Action      string               `json:"action,omitempty"`
	Source      string               `json:"source,omitempty"`
	Target      string               `json:"target,omitempty"`
	SourceState *OperationState      `json:"source_state,omitempty"`
	TargetState *OperationState      `json:"target_state,omitempty"`
	At          int64                `json:"at"`
	Checkpoint  *OperationCheckpoint `json:"checkpoint,omitempty"`
	Error       string               `json:"error,omitempty"`
}

// OperationState describes a path as observed with Lstat. A missing or
// unreadable path is explicit; a directory's contents are never embedded.
type OperationState struct {
	Kind  string `json:"kind"` // file, dir, link, other, missing, unknown
	Size  int64  `json:"size,omitempty"`
	MTime int64  `json:"mtime_ns,omitempty"`
}

func operationState(path string) *OperationState {
	if path == "" {
		return nil
	}
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &OperationState{Kind: "missing"}
		}
		return &OperationState{Kind: "unknown"}
	}
	kind := "other"
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		kind = "link"
	case info.IsDir():
		kind = "dir"
	case info.Mode().IsRegular():
		kind = "file"
	}
	return &OperationState{Kind: kind, Size: info.Size(), MTime: info.ModTime().UnixNano()}
}

type OperationLog struct {
	mu          sync.Mutex
	journal     *Journal
	filename    string
	pending     map[uint64]Operation
	active      map[uint64]bool
	rebuilding  atomic.Bool
	recoveryErr error
	onChange    func([]string)
	history     map[string]bool
	checkpoint  *OperationCheckpoint
}

// OpenOperationLog opens a durable intent stream. Unlike an index delta log,
// this stream survives snapshot generations. Consumers reconcile ReplayPaths
// and completion notifications; checkpoint only after a fenced full rebuild.
func OpenOperationLog(filename string) (*OperationLog, error) {
	return openOperationLog(filename, false)
}

// OpenRecoverableOperationLog retains exclusive ownership of damaged logs.
// Begin stays disabled until a successful full scan has installed a checkpoint.
func OpenRecoverableOperationLog(filename string) (*OperationLog, error) {
	return openOperationLog(filename, true)
}
func openOperationLog(filename string, recovery bool) (*OperationLog, error) {
	filename, err := filepath.Abs(filename)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(filename), 0o700); err != nil {
		return nil, err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(filename))
	if err != nil {
		return nil, err
	}
	filename = filepath.Join(parent, filepath.Base(filename))
	identity := sha256.Sum256([]byte("filesync-local-operations-v1"))
	j, err := openJournal(filename, identity, recovery)
	if err != nil {
		return nil, err
	}
	l := &OperationLog{journal: j, filename: filename, pending: make(map[uint64]Operation), active: make(map[uint64]bool), history: make(map[string]bool)}
	if err := l.Replay(func(seq uint64, op Operation) error {
		if op.Phase == "checkpoint" {
			l.checkpoint = op.Checkpoint
		}
		if op.Phase == "begin" {
			for _, p := range operationPaths(op) {
				l.history[p] = true
			}
			l.pending[seq] = op
		} else {
			delete(l.pending, op.ID)
		}
		return nil
	}); err != nil {
		if !recovery || (!errors.Is(err, ErrJournalCorrupt) && !errors.Is(err, ErrJournalBase)) {
			j.Close()
			return nil, err
		}
		l.recoveryErr = err
		j.failed = err
	}
	return l, nil
}

func validOperationAction(action string) bool {
	switch action {
	case "mkdir", "rename", "delete", "trash", "copy", "move", "download":
		return true
	}
	return false
}

func (l *OperationLog) append(op Operation) (uint64, error) {
	// JSON strings cannot preserve arbitrary Unix filename bytes. V2 encodes
	// both paths as base64; the public Operation still exposes native paths.
	op.Version = 2
	op.Source = base64.StdEncoding.EncodeToString([]byte(op.Source))
	op.Target = base64.StdEncoding.EncodeToString([]byte(op.Target))
	op.At = time.Now().UnixNano()
	payload, err := json.Marshal(op)
	if err != nil {
		return 0, err
	}
	return l.journal.Append(payload)
}

// Begin must finish successfully before touching the filesystem.
func (l *OperationLog) Begin(action, source, target string) (uint64, error) {
	if l.rebuilding.Load() {
		return 0, errors.New("正在完整重建索引，请完成后重试文件操作")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.rebuilding.Load() {
		return 0, errors.New("正在完整重建索引，请完成后重试文件操作")
	}
	if l.recoveryErr != nil {
		return 0, fmt.Errorf("日志需要恢复，请在本机索引中选择全部重建: %w", l.recoveryErr)
	}
	_, size, err := l.journal.position()
	if err != nil {
		return 0, err
	}
	if size >= maxOperationLogSize {
		return 0, errors.New("文件操作日志已达到容量限制，请先全部重建本机索引以建立检查点")
	}
	if !validOperationAction(action) || source == "" && target == "" {
		return 0, errors.New("无效的本机文件操作")
	}
	for _, p := range []string{source, target} {
		if p != "" && (!filepath.IsAbs(p) || filepath.Clean(p) != p) {
			return 0, fmt.Errorf("文件操作路径必须为规范的绝对路径: %s", p)
		}
	}
	if target != "" {
		if err := l.CheckWritePath(target); err != nil {
			return 0, err
		}
	}
	resolvedSource, err := resolveOperationPath(source)
	if err != nil {
		return 0, err
	}
	resolvedTarget, err := resolveOperationPath(target)
	if err != nil {
		return 0, err
	}
	for _, protected := range []string{l.filename, l.filename + ".lock"} {
		destructive := action == "rename" || action == "move" || action == "delete" || action == "trash"
		if destructive && resolvedSource != "" && Within(protected, resolvedSource) {
			return 0, errors.New("不能移动或删除正在使用的本机操作日志或其上级目录")
		}
		// Directory downloads merge arbitrary children just like directory copies.
		if resolvedTarget != "" && Within(protected, resolvedTarget) {
			return 0, errors.New("不能写入正在使用的本机操作日志或其上级目录")
		}
	}
	op := Operation{Phase: "begin", Action: action, Source: source, Target: target, SourceState: operationState(source), TargetState: operationState(target)}
	id, err := l.append(op)
	if err == nil {
		l.pending[id] = op
		l.active[id] = true
		for _, p := range operationPaths(op) {
			l.history[p] = true
		}
	}
	return id, err
}

// Finish records the result. An error does not imply no filesystem changes:
// e.g. a canceled copy may have written part of a directory tree.
func (l *OperationLog) Finish(id uint64, opErr error) error {
	l.mu.Lock()
	var changed []string
	defer func() {
		fn := l.onChange
		l.mu.Unlock()
		if fn != nil && len(changed) > 0 {
			fn(changed)
		}
	}()
	defer delete(l.active, id)
	start, ok := l.pending[id]
	if !ok {
		return errors.New("无效的文件操作序号")
	}
	changed = operationPaths(start)
	op := Operation{Phase: "done", ID: id, SourceState: operationState(start.Source), TargetState: operationState(start.Target)}
	if opErr != nil {
		op.Phase = "failed"
		op.Error = opErr.Error()
	}
	_, err := l.append(op)
	if err == nil {
		delete(l.pending, id)
	}
	return err
}

// Replay validates record schema and begin/end relationships before handing
// each record to the caller. An unmatched begin remains visibly pending.
func (l *OperationLog) Replay(fn func(uint64, Operation) error) error {
	started := map[uint64]bool{}
	return l.journal.Replay(func(seq uint64, data []byte) error {
		var op Operation
		if err := json.Unmarshal(data, &op); err != nil || (op.Version != 1 && op.Version != 2) || op.At <= 0 {
			return ErrJournalCorrupt
		}
		if op.Version == 2 {
			src, err := base64.StdEncoding.Strict().DecodeString(op.Source)
			if err != nil {
				return ErrJournalCorrupt
			}
			dst, err := base64.StdEncoding.Strict().DecodeString(op.Target)
			if err != nil {
				return ErrJournalCorrupt
			}
			op.Source, op.Target = string(src), string(dst)
		}
		switch op.Phase {
		case "checkpoint":
			if seq != 1 || op.Checkpoint == nil || op.Checkpoint.Generation == "" || len(op.Checkpoint.Snapshot) != 64 || len(op.Checkpoint.Scope) != 64 || op.ID != 0 || op.Source != "" || op.Target != "" || op.Action != "" {
				return ErrJournalCorrupt
			}
			if _, err := hex.DecodeString(op.Checkpoint.Snapshot); err != nil {
				return ErrJournalCorrupt
			}
			if _, err := hex.DecodeString(op.Checkpoint.Scope); err != nil {
				return ErrJournalCorrupt
			}
		case "begin":
			if op.ID != 0 || !validOperationAction(op.Action) || op.Source == "" && op.Target == "" {
				return ErrJournalCorrupt
			}
			for _, p := range []string{op.Source, op.Target} {
				if p != "" && (!filepath.IsAbs(p) || filepath.Clean(p) != p) {
					return ErrJournalCorrupt
				}
			}
			started[seq] = true
		case "done", "failed":
			if !started[op.ID] || op.Action != "" || op.Source != "" || op.Target != "" {
				return ErrJournalCorrupt
			}
			delete(started, op.ID)
		default:
			return ErrJournalCorrupt
		}
		if fn != nil {
			return fn(seq, op)
		}
		return nil
	})
}

func (l *OperationLog) Close() error { return l.journal.Close() }

// Resolve existing ancestors as well as the leaf. Targets need not exist yet.
// Errors other than absence must not disable protection.
func resolveOperationPath(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		return resolved, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	parent := filepath.Dir(path)
	if parent == path {
		return "", err
	}
	resolved, err = resolveOperationPath(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolved, filepath.Base(path)), nil
}

// Checkpoints apply only to this exact snapshot and scope. Sequence numbers
// are local to Generation; consumers must never compare bare IDs across it.
type OperationCheckpoint struct {
	Generation       string `json:"generation"`
	Snapshot         string `json:"snapshot"`
	Scope            string `json:"scope"`
	PreviousSequence uint64 `json:"previous_sequence"`
}

// StartCheckpoint is nonblocking: live filesystem operations must finish
// first. Unfinished intents from previous processes can be reconciled by the
// full scan. The caller must call EndCheckpoint on every exit path.
func (l *OperationLog) StartCheckpoint() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.rebuilding.Load() || len(l.active) > 0 {
		return errors.New("请等待本机文件操作结束后再全部重建索引")
	}
	l.rebuilding.Store(true)
	return nil
}
func (l *OperationLog) EndCheckpoint() { l.mu.Lock(); l.rebuilding.Store(false); l.mu.Unlock() }

// CommitCheckpoint must follow a successful, error-free full scan and saved,
// validated snapshot, while StartCheckpoint excludes application mutations.
// External writers still require a subsequent scan; this is not an OS watcher.
func (l *OperationLog) CommitCheckpoint(ctx context.Context, snapshotFile string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.rebuilding.Load() {
		return errors.New("尚未开始索引检查点")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	f, err := os.Open(snapshotFile)
	if err != nil {
		return err
	}
	defer f.Close()
	header := make([]byte, snapshotHeaderSize)
	if _, err = io.ReadFull(f, header); err != nil {
		return err
	}
	// Hash the payload and signed header, matching the snapshot format.
	h := sha256.New()
	buf := make([]byte, 256*1024)
	for {
		if err = ctx.Err(); err != nil {
			return err
		}
		n, e := f.Read(buf)
		h.Write(buf[:n])
		if e == io.EOF {
			break
		}
		if e != nil {
			return e
		}
	}
	h.Write(header[:72])
	digest := h.Sum(nil)
	if string(header[:8]) != string(snapshotMagic[:]) || string(digest) != string(header[72:104]) || binary.LittleEndian.Uint32(header[12:16]) != 0 {
		return ErrSnapshotFormat
	}
	if err = syncJournalDirectory(filepath.Dir(snapshotFile)); err != nil {
		return err
	}
	sequence, _, err := l.journal.position()
	// Recovery handles deliberately retain a failed journal; its last valid
	// sequence is diagnostic only, not a replay watermark.
	if errors.Is(err, ErrJournalClosed) {
		return err
	}
	op := Operation{Version: 2, Phase: "checkpoint", At: time.Now().UnixNano(), Checkpoint: &OperationCheckpoint{Generation: rand.Text(), Snapshot: hex.EncodeToString(digest), Scope: hex.EncodeToString(header[40:72]), PreviousSequence: sequence}}
	payload, err := json.Marshal(op)
	if err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = l.journal.replace(sha256.Sum256([]byte("filesync-local-operations-v1")), payload); err != nil {
		return err
	}
	l.pending = make(map[uint64]Operation)
	l.history = make(map[string]bool)
	l.checkpoint = op.Checkpoint
	l.recoveryErr = nil
	return nil
}
func (l *OperationLog) RecoveryNeeded() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.recoveryErr != nil
}

// CheckWritePath rechecks each actual destination of a recursive operation.
// Call immediately before touching the path, including directory creation.
func (l *OperationLog) CheckWritePath(path string) error {
	if l == nil {
		return nil
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	resolved, err := resolveOperationPath(abs)
	if err != nil {
		return err
	}
	for _, protected := range []string{l.filename, l.filename + ".lock"} {
		if Within(protected, resolved) {
			return errors.New("不能覆盖正在使用的本机操作日志或其上级目录")
		}
		actual, e1 := os.Stat(resolved)
		held, e2 := os.Stat(protected)
		if e1 == nil && e2 == nil && os.SameFile(actual, held) {
			return errors.New("目标指向正在使用的本机操作日志")
		}
	}
	return nil
}

// Limit new intents, but always allow in-flight outcomes to be recorded.
// Full rebuild is the explicit, safe way to reclaim this space.
const maxOperationLogSize = 64 << 20

func operationPaths(op Operation) []string {
	var paths []string
	if op.Source != "" && (op.Action == "rename" || op.Action == "move" || op.Action == "delete" || op.Action == "trash") {
		paths = append(paths, op.Source)
	}
	if op.Target != "" {
		paths = append(paths, op.Target)
	}
	return paths
}

// SetOnChange installs a nonblocking notification. It runs after Finish has
// released the log lock, including when the outcome write failed.
func (l *OperationLog) SetOnChange(fn func([]string)) { l.mu.Lock(); l.onChange = fn; l.mu.Unlock() }

// ReplayPaths returns paths to reconcile, including unfinished/failed intents.
// A mismatched checkpoint requires the whole current scope to be reconciled:
// records before that checkpoint were already removed. No historical mutation
// is executed, so duplicate records and a newer base are safe.
func (l *OperationLog) ReplayPaths(ctx context.Context, snapshotFile string, scopes ...ScanOptions) ([]string, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	l.mu.Lock()
	if l.recoveryErr != nil {
		err := l.recoveryErr
		l.mu.Unlock()
		return nil, false, err
	}
	paths := make([]string, 0, len(l.history))
	for p := range l.history {
		paths = append(paths, p)
	}
	checkpoint := l.checkpoint
	l.mu.Unlock()
	mismatch := false
	if checkpoint != nil {
		f, err := os.Open(snapshotFile)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				return nil, false, err
			}
			mismatch = true
		} else {
			header := make([]byte, snapshotHeaderSize)
			_, err = io.ReadFull(f, header)
			f.Close()
			mismatch = err != nil || hex.EncodeToString(header[72:104]) != checkpoint.Snapshot || hex.EncodeToString(header[40:72]) != checkpoint.Scope
		}
	}
	if checkpoint != nil && len(scopes) > 0 {
		scope, err := scopeDigest(scopes[0])
		if err != nil {
			return nil, false, err
		}
		mismatch = mismatch || hex.EncodeToString(scope[:]) != checkpoint.Scope
	}
	return collapsePaths(paths), mismatch, nil
}
