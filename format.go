package main

import (
	"fmt"
	"math"
)

func formatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	v := float64(n)
	for _, u := range []string{"KB", "MB", "GB", "TB"} {
		v /= unit
		if v < unit || u == "TB" {
			if v < 10 {
				return fmt.Sprintf("%.2f %s", v, u)
			}
			return fmt.Sprintf("%.1f %s", v, u)
		}
	}
	return ""
}

func formatDuration(seconds float64) string {
	s := int(math.Round(seconds))
	switch {
	case s < 60:
		return fmt.Sprintf("%d 秒", s)
	case s < 3600:
		return fmt.Sprintf("%d 分 %d 秒", s/60, s%60)
	default:
		return fmt.Sprintf("%d 小时 %d 分", s/3600, s%3600/60)
	}
}
