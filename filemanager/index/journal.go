package index

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"sync"
)

// Journal is an append-only, checksummed log bound to a caller-supplied stream
// identity (for example a snapshot digest or a stable intent-stream ID).
// Only one process can own a journal at a time. A record is durable after Append
// returns nil. The caller decides how records are interpreted and checkpointed.
type Journal struct {
	mu       sync.Mutex
	file     *os.File
	lock     *os.File
	filename string
	size     int64
	sequence uint64
	failed   error
}

var (
	ErrJournalCorrupt = errors.New("索引变更日志不完整或已损坏")
	ErrJournalBase    = errors.New("索引日志身份不匹配")
	ErrJournalClosed  = errors.New("索引变更日志已关闭")
)

var journalMagic = [8]byte{'F', 'S', 'I', 'D', 'X', 'L', 'O', 'G'}

const (
	journalHeaderSize = 40 // magic and 32-byte stream identity
	journalFrameSize  = 16 // payload length, sequence, checksum
	maxJournalRecord  = 4 << 20
)

// OpenJournal checks the entire log before returning it. An incomplete final
// frame is reported as corruption: silently dropping it could lose a change
// that reached disk before the process crashed. The caller must reconcile it.
func OpenJournal(filename string, snapshotDigest [32]byte) (*Journal, error) {
	return openJournal(filename, snapshotDigest, false)
}
func openJournal(filename string, snapshotDigest [32]byte, recovery bool) (*Journal, error) {
	// Reject redirected journal/lock leaves; the operation layer canonicalizes
	// the parent. Identity is checked again around every append.
	for _, p := range []string{filename, filename + ".lock"} {
		st, err := os.Lstat(p)
		if err == nil && !st.Mode().IsRegular() {
			return nil, fmt.Errorf("日志路径不是普通文件: %s", p)
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	lock, err := os.OpenFile(filename+".lock", os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err = lockJournal(lock); err != nil {
		lock.Close()
		return nil, fmt.Errorf("索引日志已被其他进程使用或无法加锁: %w", err)
	}
	f, err := os.OpenFile(filename, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	created := err == nil
	if errors.Is(err, os.ErrExist) {
		f, err = os.OpenFile(filename, os.O_RDWR, 0)
	}
	if err != nil {
		lock.Close()
		return nil, err
	}
	j := &Journal{file: f, lock: lock, filename: filename}
	if err := j.open(snapshotDigest, created); err != nil {
		if !recovery || (!errors.Is(err, ErrJournalCorrupt) && !errors.Is(err, ErrJournalBase)) {
			f.Close()
			lock.Close()
			return nil, err
		}
		j.failed = err
	}
	if err := syncJournalDirectory(filepath.Dir(filename)); err != nil {
		f.Close()
		lock.Close()
		return nil, fmt.Errorf("同步索引日志目录: %w", err)
	}
	return j, nil
}

func (j *Journal) open(snapshotDigest [32]byte, created bool) error {
	stat, err := j.file.Stat()
	if err != nil {
		return err
	}
	if created {
		var header [journalHeaderSize]byte
		copy(header[:8], journalMagic[:])
		copy(header[8:], snapshotDigest[:])
		if _, err = j.file.WriteAt(header[:], 0); err != nil {
			return err
		}
		if err = j.file.Sync(); err != nil {
			return err
		}
		j.size = journalHeaderSize
		return nil
	}
	if stat.Size() < journalHeaderSize {
		return ErrJournalCorrupt
	}
	var header [journalHeaderSize]byte
	if _, err := j.file.ReadAt(header[:], 0); err != nil {
		return err
	}
	if string(header[:8]) != string(journalMagic[:]) {
		return ErrJournalCorrupt
	}
	if string(header[8:]) != string(snapshotDigest[:]) {
		return ErrJournalBase
	}
	j.size = stat.Size()
	return j.read(nil)
}

// Replay visits complete records in commit order. The callback must not call
// back into this journal. Returning an error stops replay without changing it.
func (j *Journal) Replay(fn func(sequence uint64, payload []byte) error) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.file == nil {
		return ErrJournalClosed
	}
	if j.failed != nil {
		return j.failed
	}
	return j.read(fn)
}

func (j *Journal) read(fn func(uint64, []byte) error) error {
	offset, seq := int64(journalHeaderSize), uint64(0)
	for offset < j.size {
		if j.size-offset < journalFrameSize {
			return ErrJournalCorrupt
		}
		var frame [journalFrameSize]byte
		if _, err := j.file.ReadAt(frame[:], offset); err != nil {
			return fmt.Errorf("读取索引日志: %w", err)
		}
		length := binary.LittleEndian.Uint32(frame[:4])
		next := binary.LittleEndian.Uint64(frame[4:12])
		if length > maxJournalRecord || next != seq+1 || int64(length) > j.size-offset-journalFrameSize {
			return ErrJournalCorrupt
		}
		payload := make([]byte, length)
		if _, err := j.file.ReadAt(payload, offset+journalFrameSize); err != nil {
			return fmt.Errorf("读取索引日志: %w", err)
		}
		h := crc32.NewIEEE()
		h.Write(frame[:12])
		h.Write(payload)
		if h.Sum32() != binary.LittleEndian.Uint32(frame[12:]) {
			return ErrJournalCorrupt
		}
		if fn != nil {
			if err := fn(next, payload); err != nil {
				return err
			}
		}
		seq, offset = next, offset+journalFrameSize+int64(length)
	}
	j.sequence = seq
	return nil
}

// Append serializes writers, records a checksum over the frame and payload,
// and syncs before reporting success. Any failed write or sync poisons the
// handle: its durable state is uncertain and must be inspected after reopen.
func (j *Journal) Append(payload []byte) (uint64, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.file == nil {
		return 0, ErrJournalClosed
	}
	if j.failed != nil {
		return 0, j.failed
	}
	if err := j.checkIdentity(); err != nil {
		j.failed = err
		return 0, err
	}
	st, err := j.file.Stat()
	if err != nil {
		j.failed = err
		return 0, err
	}
	if st.Size() != j.size {
		j.failed = errors.New("日志文件长度已被外部修改")
		return 0, j.failed
	}
	if len(payload) > maxJournalRecord || j.sequence == ^uint64(0) {
		return 0, fmt.Errorf("索引日志记录过大或序号耗尽")
	}
	frame := make([]byte, journalFrameSize+len(payload))
	binary.LittleEndian.PutUint32(frame[:4], uint32(len(payload)))
	seq := j.sequence + 1
	binary.LittleEndian.PutUint64(frame[4:12], seq)
	copy(frame[journalFrameSize:], payload)
	h := crc32.NewIEEE()
	h.Write(frame[:12])
	h.Write(payload)
	binary.LittleEndian.PutUint32(frame[12:], h.Sum32())
	if _, err := j.file.WriteAt(frame, j.size); err != nil {
		j.failed = fmt.Errorf("写入索引日志: %w", err)
		return 0, j.failed
	}
	if err := j.file.Sync(); err != nil {
		j.failed = fmt.Errorf("同步索引日志: %w", err)
		return 0, j.failed
	}
	if err := j.checkIdentity(); err != nil {
		j.failed = err
		return 0, err
	}
	j.sequence, j.size = seq, j.size+int64(len(frame))
	return seq, nil
}

func (j *Journal) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.file == nil && j.lock == nil {
		return nil
	}
	var err error
	if j.file != nil {
		err = j.file.Close()
	}
	j.file = nil
	if lockErr := j.lock.Close(); err == nil {
		err = lockErr
	}
	j.lock = nil
	return err
}

// An open descriptor can survive unlink/rename. Never report durable success
// if the journal or its lock no longer names the file we actually hold.
func (j *Journal) checkIdentity() error {
	for _, pair := range []struct {
		path string
		f    *os.File
	}{{j.filename, j.file}, {j.filename + ".lock", j.lock}} {
		held, err := pair.f.Stat()
		if err != nil {
			return err
		}
		current, err := os.Lstat(pair.path)
		if err != nil {
			return fmt.Errorf("日志文件路径已改变: %w", err)
		}
		if !current.Mode().IsRegular() || !os.SameFile(held, current) {
			return errors.New("日志文件或锁已被替换")
		}
	}
	return nil
}

// replace installs a new checkpoint while retaining the exclusive lock. The
// old file remains authoritative until a fully synced replacement is renamed.
// Corrupt bytes are preserved before replacement, never silently truncated.
func (j *Journal) replace(identity [32]byte, payload []byte) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.file == nil {
		return ErrJournalClosed
	}
	if err := j.checkIdentity(); err != nil {
		return err
	}
	if j.failed != nil {
		backup, err := os.CreateTemp(filepath.Dir(j.filename), filepath.Base(j.filename)+".damaged-*")
		if err != nil {
			return err
		}
		_, err = io.Copy(backup, io.NewSectionReader(j.file, 0, 1<<63-1))
		if err == nil {
			err = backup.Sync()
		}
		closeErr := backup.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			return err
		}
		if err = syncJournalDirectory(filepath.Dir(j.filename)); err != nil {
			return err
		}
	}
	f, err := os.CreateTemp(filepath.Dir(j.filename), ".operations-checkpoint-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	defer f.Close()
	next := &Journal{file: f, lock: j.lock, filename: tmp}
	if err = next.open(identity, true); err != nil {
		return err
	}
	// Build a frame directly: the temporary file deliberately shares the old
	// lock, so Append's path-identity check does not apply here.
	frame := make([]byte, journalFrameSize+len(payload))
	binary.LittleEndian.PutUint32(frame[:4], uint32(len(payload)))
	binary.LittleEndian.PutUint64(frame[4:12], 1)
	copy(frame[journalFrameSize:], payload)
	h := crc32.NewIEEE()
	h.Write(frame[:12])
	h.Write(payload)
	binary.LittleEndian.PutUint32(frame[12:16], h.Sum32())
	if _, err = f.WriteAt(frame, journalHeaderSize); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = j.checkIdentity(); err != nil {
		return err
	}
	// Windows does not permit replacing a file opened without delete sharing.
	// Keep the separate exclusive lock, but close the data handle before rename.
	if err = j.file.Close(); err != nil {
		j.failed = err
		return err
	}
	j.file = nil
	renameErr := os.Rename(tmp, j.filename)
	j.file, err = os.OpenFile(j.filename, os.O_RDWR, 0)
	if renameErr != nil {
		// Original bytes were not replaced. Poison the handle rather than risk
		// appending if an external writer changed its identity while it was closed.
		j.failed = renameErr
		return renameErr
	}
	// After rename never use the old descriptor, even if directory sync fails.
	if err != nil {
		j.failed = err
		return err
	}
	j.size, j.sequence = journalHeaderSize+int64(len(frame)), 1
	j.failed = syncJournalDirectory(filepath.Dir(j.filename))
	return j.failed
}

func (j *Journal) position() (uint64, int64, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.file == nil {
		return j.sequence, j.size, ErrJournalClosed
	}
	return j.sequence, j.size, j.failed
}

// Sequence returns the last durable sequence. Callers must fence filesystem
// writers as well when using it to retire an index overlay.
func (j *Journal) Sequence() (uint64, error) { seq, _, err := j.position(); return seq, err }
