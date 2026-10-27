package fileindex

import (
	"context"
	"errors"
)

type ArchiveResult struct {
	Root    int64  `json:"root"`
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	Skipped bool   `json:"skipped"`
}

type CompressRequest struct {
	Root     int64    `json:"root"`
	Paths    []string `json:"paths"`
	DstRoot  int64    `json:"dst_root"`
	DstDir   string   `json:"dst_dir"`
	Name     string   `json:"name"`
	Format   string   `json:"format"`
	Level    string   `json:"level"`
	Conflict string   `json:"conflict"`
}

func (c *Client) CompressArchive(ctx context.Context, req CompressRequest) (*Task, error) {
	var out struct {
		Task *Task `json:"task"`
	}
	err := c.post(ctx, "/api/archive/compress", req, &out)
	var se *StatusError
	if errors.As(err, &se) && se.Code == 404 && se.Msg == "not found" {
		return nil, errors.New("服务器暂不支持压缩，请先更新 fileindex")
	}
	if err == nil && out.Task == nil {
		return nil, errors.New("服务器没有返回压缩任务")
	}
	return out.Task, err
}
