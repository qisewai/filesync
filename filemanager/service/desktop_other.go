//go:build !windows

package service

import "filemanager/api"

func newDesktopService() (*Service, error) {
	return New([]api.Root{{ID: "local", Name: "本机", Path: "/"}})
}
