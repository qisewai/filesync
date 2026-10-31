package index

import "context"

// Backend separates index ownership from its consumers. A remote implementation
// keeps snapshots and result bitmaps in the backend process.
type Backend interface {
	Status() Status
	Scan(context.Context, ScanOptions, func(Progress)) (Progress, error)
	Update(context.Context, ScanOptions, func(Progress)) (Progress, error)
	LoadSnapshot(context.Context, string, ScanOptions, func(LoadProgress)) (SnapshotMeta, error)
	SaveSnapshot(context.Context, string, ScanOptions, SnapshotMeta) error
	Reconcile(context.Context, ScanOptions, []string) error
	ClearDeltas() error
	RefreshPaths() []string
	Search(context.Context, Query) (*Results, error)
	Subtree(string) (Subtree, bool)
	Close()
}

func NewRemote(b Backend) *Index { return &Index{remote: b} }
func (x *Index) Close() {
	if x.remote != nil {
		x.remote.Close()
	}
}

// Close releases a remote result lease. It is safe to call more than once.
func (r *Results) Close() error {
	if r == nil {
		return nil
	}
	r.closeOnce.Do(func() {
		r.closed.Store(true)
		if r.release != nil {
			r.closeErr = r.release()
		}
	})
	return r.closeErr
}

func RemoteResults(total int, version uint64, page func(context.Context, int, int) ([]Entry, error), release ...func() error) *Results {
	r := &Results{total: total, remoteVersion: version, remotePage: page}
	if len(release) > 0 {
		r.release = release[0]
	}
	return r
}
