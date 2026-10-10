// Package engine compares two directories and copies one onto the other.
//
// A backup runs in two steps: Prepare scans both directories and returns a
// Plan of what would change, which the user reviews, and Execute carries it
// out. Neither step needs a user interface.
package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

// Mode is how a backup treats what the target has that the source has not.
type Mode int

const (
	// Incremental copies new and changed files and keeps the target's other
	// files.
	Incremental Mode = iota
	// Mirror makes the target the same as the source, deleting the target's
	// files that the source has not.
	Mirror
)

func (m Mode) String() string {
	if m == Mirror {
		return "完全一致"
	}
	return "增量备份"
}

// DefaultTimeTolerance is how far apart two modification times may be for
// the files to count as unchanged: FAT keeps times to 2 seconds, and SMB
// and FTP mounts round them too.
const DefaultTimeTolerance = 2 * time.Second

// Options describe a backup.
type Options struct {
	Source, Target string
	Mode           Mode
	Excludes       []string
	// TimeTolerance is DefaultTimeTolerance when zero.
	TimeTolerance time.Duration
	// Dest is where the backup writes, in place of the local directory
	// Target, as a server.
	Dest Target
}

// ActionKind is what an action does.
type ActionKind int

const (
	ActCreate  ActionKind = iota // copy a file the target has not
	ActUpdate                    // copy a file over the target's older one
	ActMkdir                     // make a directory
	ActDelete                    // delete a file from the target
	ActRmdir                     // delete an empty directory from the target
	ActSkip                      // leave a source entry that cannot be copied
	ActCleanup                   // delete a temporary file an earlier run left
)

func (k ActionKind) String() string {
	return [...]string{"新增", "更新", "建目录", "删除", "删目录", "跳过", "清理"}[k]
}

// Action is one change of a plan.
type Action struct {
	Kind ActionKind
	// Path is relative to the roots, with slashes.
	Path    string
	Size    int64
	ModTime time.Time
	// Reason says why an entry is skipped, or why a target entry is deleted
	// when it is not simply missing from the source.
	Reason string

	perm os.FileMode
}

// Summary counts a plan's actions.
type Summary struct {
	Create, Update, Mkdir, Delete, Rmdir, Skip, Cleanup int
	// CopyBytes is the size of the files to copy.
	CopyBytes int64
}

// Changes is the number of actions that change the target.
func (s Summary) Changes() int {
	return s.Create + s.Update + s.Mkdir + s.Delete + s.Rmdir + s.Cleanup
}

// Plan is what a backup would do. Actions are in the order Execute performs
// them: deletions first, deepest first, then directories, then copies, with
// skipped entries where the source has them.
type Plan struct {
	Options Options
	Actions []Action
	Summary Summary
	// Warnings are what the user should know before running the plan.
	Warnings []string
	// SourceFiles and TargetFiles count the files found, excluded ones left
	// out.
	SourceFiles, TargetFiles int
	// MassDelete is set when a mirror would delete more than half of the
	// target's files, as when the source is a share that is not mounted:
	// ask the user again before running it.
	MassDelete bool
	// CaseInsensitive reports whether the target folds the case of names.
	CaseInsensitive bool
	// TargetMissing is set when the target directory does not exist yet.
	TargetMissing bool
	Created       time.Time

	source string // the resolved source directory
	dest   Target
}

// Source is the directory the plan copies from.
func (p *Plan) Source() string { return p.source }

// ScanProgress is reported while Prepare scans.
type ScanProgress struct {
	// Entries counts what was listed in both directories so far.
	Entries int
}

// Prepare scans the source and the target and plans the backup. onProgress,
// which may be nil, is called now and then from the calling goroutine.
func Prepare(ctx context.Context, opts Options, onProgress func(ScanProgress)) (*Plan, error) {
	if opts.TimeTolerance <= 0 {
		opts.TimeTolerance = DefaultTimeTolerance
	}
	source, err := checkSource(opts.Source)
	if err != nil {
		return nil, err
	}
	dest := opts.Dest
	if dest == nil {
		if dest, err = newLocalTarget(source, opts.Target); err != nil {
			return nil, err
		}
	}
	filter, err := NewFilter(opts.Excludes)
	if err != nil {
		return nil, err
	}

	var n int
	last := time.Now()
	onEntry := func() {
		n++
		if onProgress != nil && n%256 == 0 && time.Since(last) > 100*time.Millisecond {
			last = time.Now()
			onProgress(ScanProgress{Entries: n})
		}
	}
	src, err := scan(ctx, source, filter, onEntry)
	if err != nil {
		return nil, fmt.Errorf("无法读取源目录: %w", err)
	}
	dst, missing, err := dest.Scan(ctx, filter, onEntry)
	if err != nil {
		return nil, fmt.Errorf("无法读取目标 %s: %w", dest, err)
	}
	if onProgress != nil {
		onProgress(ScanProgress{Entries: n})
	}

	p := &Plan{
		Options:         opts,
		CaseInsensitive: dest.CaseInsensitive(),
		TargetMissing:   missing,
		Created:         time.Now(),
		source:          source,
		dest:            dest,
	}
	p.build(src, dst)
	if p.Options.Mode == Mirror && p.SourceFiles == 0 && len(src.Entries) == 0 && p.TargetFiles > 0 {
		return nil, errors.New("源目录是空的，而目标目录有文件。完全一致模式会删除目标中的所有文件，已拒绝执行。请确认源目录（如网络共享）已经挂载")
	}
	return p, nil
}

// checkSource resolves the source directory.
func checkSource(source string) (string, error) {
	if strings.TrimSpace(source) == "" {
		return "", errors.New("请选择源目录")
	}
	src, err := filepath.Abs(source)
	if err != nil {
		return "", err
	}
	if src, err = filepath.EvalSymlinks(src); err != nil {
		return "", fmt.Errorf("无法访问源目录 %s: %w", source, err)
	}
	info, err := os.Stat(src)
	if err != nil {
		return "", fmt.Errorf("无法访问源目录 %s: %w", source, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("源 %s 不是目录", source)
	}
	return src, nil
}

// within reports whether p is dir or inside it.
func within(p, dir string) bool {
	if runtime.GOOS == "windows" {
		p, dir = strings.ToLower(p), strings.ToLower(dir)
	}
	rel, err := filepath.Rel(dir, p)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func (p *Plan) build(src, dst *Tree) {
	key := func(s string) string { return s }
	if p.CaseInsensitive {
		key = strings.ToLower
	}
	checkNames := runtime.GOOS == "windows" || p.CaseInsensitive
	mirror := p.Options.Mode == Mirror
	tolerance := p.Options.TimeTolerance

	dstIndex := make(map[string]*Entry, len(dst.Entries))
	for i := range dst.Entries {
		e := &dst.Entries[i]
		dstIndex[key(e.Path)] = e
		if !e.IsDir {
			p.TargetFiles++
		}
	}
	unreadable := map[string]bool{}
	for _, rel := range src.Unreadable {
		unreadable[key(rel)] = true
	}

	var (
		keep     = map[string]bool{} // target entries that stay
		skipped  = map[string]bool{} // source directories left out, with their contents
		conflict = map[string]bool{} // target entries of another type than the source's
		seen     = map[string]string{}
		deletes  []Action
		creates  []Action
	)
	for i := range src.Entries {
		e := &src.Entries[i]
		k := key(e.Path)
		if !e.IsDir {
			p.SourceFiles++
		}
		if parent := path.Dir(e.Path); parent != "." && skipped[key(parent)] {
			continue
		}
		skip := func(reason string) {
			creates = append(creates, Action{Kind: ActSkip, Path: e.Path, Size: e.Size, Reason: reason})
			if e.IsDir {
				skipped[k] = true
			}
			if _, ok := seen[k]; !ok {
				keep[k] = true // what the target has in its place stays
			}
		}
		if e.Special != "" {
			skip(e.Special)
			continue
		}
		if other, ok := seen[k]; ok {
			skip(fmt.Sprintf("与 %s 只有大小写不同，目标不区分大小写", other))
			continue
		}
		if checkNames {
			if problem := windowsNameProblem(path.Base(e.Path)); problem != "" {
				skip(problem)
				continue
			}
		}
		seen[k] = e.Path

		d := dstIndex[k]
		if d != nil && (d.IsDir != e.IsDir || d.Special != "") {
			if !mirror {
				skip("目标中已有同名但类型不同的项，增量备份不会删除它")
				continue
			}
			// It goes, with what it holds, before the source's is copied.
			conflict[k] = true
			d = nil
		}
		if d != nil {
			keep[k] = true
		}
		switch {
		case e.IsDir && d == nil:
			creates = append(creates, Action{Kind: ActMkdir, Path: e.Path, perm: e.Perm})
		case e.IsDir:
		case d == nil:
			creates = append(creates, Action{Kind: ActCreate, Path: e.Path, Size: e.Size, ModTime: e.ModTime, perm: e.Perm})
		case d.Size != e.Size || absDuration(d.ModTime.Sub(e.ModTime)) > tolerance:
			creates = append(creates, Action{Kind: ActUpdate, Path: e.Path, Size: e.Size, ModTime: e.ModTime, perm: e.Perm})
		}
	}

	if mirror {
		for i := range dst.Entries {
			d := &dst.Entries[i]
			k := key(d.Path)
			if keep[k] || underAny(d.Path, unreadable, key) || underAny(d.Path, skipped, key) {
				continue
			}
			reason := ""
			if conflict[k] {
				reason = "与源中同名项的类型不同"
			}
			deletes = append(deletes, deleteAction(d, reason))
		}
	}
	for _, rel := range dst.Temps {
		deletes = append(deletes, Action{Kind: ActCleanup, Path: rel, Reason: "上次中断时留下的临时文件"})
	}
	if len(src.Unreadable) > 0 && mirror {
		p.Warnings = append(p.Warnings, fmt.Sprintf("源中有 %d 个目录无法读取，目标中对应的内容不会被删除", len(src.Unreadable)))
	}
	for _, msg := range src.Errors {
		p.Warnings = append(p.Warnings, "读取源出错: "+msg)
	}
	for _, msg := range dst.Errors {
		p.Warnings = append(p.Warnings, "读取目标出错: "+msg)
	}

	// Files before directories, and directories deepest first, so that each
	// is empty when it goes.
	sort.SliceStable(deletes, func(i, j int) bool {
		a, b := deletes[i], deletes[j]
		if (a.Kind == ActRmdir) != (b.Kind == ActRmdir) {
			return a.Kind != ActRmdir
		}
		if a.Kind == ActRmdir {
			return strings.Count(a.Path, "/") > strings.Count(b.Path, "/")
		}
		return false
	})
	p.Actions = append(deletes, creates...)
	for _, a := range p.Actions {
		switch a.Kind {
		case ActCreate:
			p.Summary.Create++
			p.Summary.CopyBytes += a.Size
		case ActUpdate:
			p.Summary.Update++
			p.Summary.CopyBytes += a.Size
		case ActMkdir:
			p.Summary.Mkdir++
		case ActDelete:
			p.Summary.Delete++
		case ActRmdir:
			p.Summary.Rmdir++
		case ActSkip:
			p.Summary.Skip++
		case ActCleanup:
			p.Summary.Cleanup++
		}
	}
	if mirror && p.TargetFiles >= 10 && p.Summary.Delete*2 > p.TargetFiles {
		p.MassDelete = true
		p.Warnings = append([]string{fmt.Sprintf("将删除目标中 %d 个文件中的 %d 个，超过一半。请确认源目录正确且已挂载", p.TargetFiles, p.Summary.Delete)}, p.Warnings...)
	}
	if p.TargetMissing {
		p.Warnings = append(p.Warnings, "目标目录不存在，将会创建")
	}
}

func deleteAction(d *Entry, reason string) Action {
	if d.IsDir {
		return Action{Kind: ActRmdir, Path: d.Path, Reason: reason}
	}
	return Action{Kind: ActDelete, Path: d.Path, Size: d.Size, Reason: reason}
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}
