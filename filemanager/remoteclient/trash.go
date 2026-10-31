package fileindex

import "context"

type TrashItem struct {
	ID        int64  `json:"id"`
	RootID    int64  `json:"root_id"`
	RootName  string `json:"root_name"`
	OrigPath  string `json:"orig_path"`
	Name      string `json:"name"`
	IsDir     bool   `json:"is_dir"`
	Size      int64  `json:"size"`
	DeletedAt int64  `json:"deleted_at"`
}

func (c *Client) ListTrash(ctx context.Context) ([]TrashItem, error) {
	var out []TrashItem
	err := c.do(ctx, "GET", "/api/trash", nil, nil, &out)
	return out, err
}
func (c *Client) RestoreTrash(ctx context.Context, ids []int64) ([]string, error) {
	var out struct {
		Restored []string `json:"restored"`
	}
	err := c.post(ctx, "/api/trash/restore", map[string]any{"ids": ids}, &out)
	return out.Restored, err
}
func (c *Client) PurgeTrash(ctx context.Context, ids []int64) (*Task, error) {
	var out struct {
		Task *Task `json:"task"`
	}
	err := c.post(ctx, "/api/trash/delete", map[string]any{"ids": ids}, &out)
	return out.Task, err
}
