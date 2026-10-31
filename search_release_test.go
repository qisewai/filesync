package main

import (
	"context"
	"filesync/config"
	"testing"
	"time"

	localindex "filemanager/index"
)

func TestSearchReleasesReplacedResults(t *testing.T) {
	h := newHarness(t, &config.Store{Dir: t.TempDir()})
	released := make(chan struct{}, 1)
	r := localindex.RemoteResults(0, 1, func(context.Context, int, int) ([]localindex.Entry, error) { return nil, nil }, func() error { released <- struct{}{}; return nil })
	h.a.gsearch.groups = []*searchGroup{{local: r}}
	h.a.gsearch.query = ""
	h.a.runSearch(0)
	select {
	case <-released:
	case <-time.After(time.Second):
		t.Fatal("old result not released")
	}
}

func TestReleasedResultCannotLoadMore(t *testing.T) {
	h := newHarness(t, &config.Store{Dir: t.TempDir()})
	h.a.gsearch.scope = localPlace
	h.a.gsearch.groups = []*searchGroup{{key: localPlace, source: localPlace, total: 1000}}
	h.a.moreResults()
	if h.a.gsearch.groups[0].more {
		t.Fatal("released result started paging")
	}
}
