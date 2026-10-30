package main

import (
	"context"
	"errors"
	"time"

	"filemanager/remoteclient"
	"github.com/egoist/mygo/ui"
)

type lanDiscovery struct {
	servers []fileindex.DiscoveredServer
	busy    bool
	err     string
	cancel  context.CancelFunc
}

func (a *app) startDiscovery() {
	if a.lan != nil && a.lan.cancel != nil {
		a.lan.cancel()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	d := &lanDiscovery{busy: true, cancel: cancel}
	a.lan = d
	browse := a.discover
	if browse == nil {
		browse = fileindex.Discover
	}
	go func() {
		defer cancel()
		err := browse(ctx, func(list []fileindex.DiscoveredServer) {
			a.post(func() {
				if !a.closed && a.nas.open && a.lan == d {
					d.servers = list
				}
			})
		})
		a.post(func() {
			if a.closed || !a.nas.open || a.lan != d {
				return
			}
			d.busy = false
			if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
				d.err = "无法发现服务器：" + err.Error()
			}
		})
	}()
}

func (a *app) stopDiscovery() {
	if a.lan != nil {
		if a.lan.cancel != nil {
			a.lan.cancel()
		}
		a.lan = nil
	}
}

func (a *app) selectDiscovered(s fileindex.DiscoveredServer) {
	// Never replace an existing endpoint/token because a LAN peer claims its ID.
	if saved := a.store.ServerByAddr(s.Addr); saved != nil {
		a.nasUse(saved)
		return
	}
	a.nasStartEdit(nil)
	a.nas.name, a.nas.addr = s.Name, s.Addr
}

func (a *app) discoveredServers(c *ui.Context) {
	d := a.lan
	if d == nil {
		return
	}
	t := c.Theme()
	ui.Column(c).Gap(6).Padding(8).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
			ui.Text(c, "局域网中的服务器").Bold().Grow(1)
			if d.busy {
				ui.Spinner(c).FontSize(14)
				ui.Text(c, "正在发现…").FontSize(12)
			}
			ui.Button(c.Key("refresh-discovery"), "重新发现").Disabled(d.busy).OnClick(a.startDiscovery)
		})
		if d.err != "" {
			ui.Text(c, d.err).TextColor(t.Danger).FontSize(12)
		}
		if len(d.servers) == 0 && !d.busy {
			ui.Text(c, "未发现服务器，请确认连接同一局域网，或手动输入地址。").FontSize(12).TextColor(t.TextMuted)
		}
		for _, s := range d.servers {
			ui.Row(c.Key("discovered/" + s.ID)).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Column(c).Grow(1).Shrink(1).Gap(2).Children(func() {
					ui.Text(c, s.Name).SingleLine()
					ui.Text(c, s.Addr).FontSize(12).TextColor(t.TextMuted)
				})
				label := "选择"
				if a.store.ServerByAddr(s.Addr) != nil {
					label = "已添加 · 打开"
				}
				ui.Button(c.Key("select/"+s.ID), label).Label("选择发现的服务器 " + s.Name + " " + s.Addr).OnClick(func() { a.selectDiscovered(s) })
			})
		}
		ui.Text(c, "发现后仍需登录；支持手动添加。").FontSize(12).TextColor(t.TextMuted)
	})
}
