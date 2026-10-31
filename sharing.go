package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"filemanager/api"
	"filemanager/localprocess"
	fileindex "filemanager/remoteclient"
	"filemanager/sharing"
	"filesync/config"
	"github.com/egoist/mygo/ui"
)

type sharingState struct {
	config     sharing.Config
	err        string
	busy       bool
	picking    bool
	process    *localprocess.ShareProcess
	cancel     context.CancelFunc
	invitation string
	connecting bool
}

func (a *app) sharingPath() string { return filepath.Join(a.store.Dir, "sharing.json") }
func (a *app) ensureSharing() {
	if a.sharing != nil {
		return
	}
	s := &sharingState{}
	a.sharing = s
	c, err := sharing.Load(a.sharingPath())
	if errors.Is(err, os.ErrNotExist) {
		c, err = sharing.NewConfig()
	}
	s.config = c
	if err != nil {
		s.err = err.Error()
	}
}
func (a *app) startSharing() {
	a.ensureSharing()
	s := a.sharing
	if s.busy || s.picking || s.process != nil {
		return
	}
	if len(s.config.Roots) == 0 {
		s.err = "请先添加要共享的磁盘或目录"
		return
	}
	if err := sharing.Save(a.sharingPath(), s.config); err != nil {
		s.err = err.Error()
		return
	}
	s.busy = true
	s.err = ""
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	s.cancel = cancel
	go func() {
		p, err := localprocess.StartShare(ctx, companionExecutable(), a.sharingPath())
		cancel()
		a.post(func() {
			s.busy = false
			s.cancel = nil
			if a.closed {
				if p != nil {
					go p.Close()
				}
				return
			}
			if err != nil {
				s.err = "无法开启共享：" + err.Error()
				return
			}
			s.process = p
			go func() {
				err := p.Wait()
				p.Close()
				a.post(func() {
					if !a.closed && s.process == p {
						s.process = nil
						s.err = "共享后台已停止"
						if err != nil {
							s.err += "：" + err.Error()
						}
					}
				})
			}()
		})
	}()
}
func (a *app) stopSharing() {
	s := a.sharing
	if s == nil || s.busy || s.process == nil {
		return
	}
	p := s.process
	s.process = nil
	s.busy = true
	go func() {
		err := p.Close()
		a.post(func() {
			s.busy = false
			if err != nil {
				s.err = err.Error()
			}
		})
	}()
}
func (a *app) addSharedDirectory() {
	s := a.sharing
	if s.picking || s.busy || s.process != nil {
		return
	}
	s.picking = true
	go func() {
		dir, err := a.pickDir("选择要共享的磁盘或目录", "")
		a.post(func() {
			s.picking = false
			if a.closed {
				return
			}
			if err != nil {
				s.err = err.Error()
				return
			}
			if dir == "" {
				return
			}
			dir, err = filepath.Abs(dir)
			if err != nil {
				s.err = err.Error()
				return
			}
			id := max(int64(1), s.config.NextRootID)
			for _, root := range s.config.Roots {
				if root.Path == dir {
					return
				}
				n, _ := strconv.ParseInt(root.ID, 10, 64)
				id = max(id, n+1)
			}
			if len(s.config.Roots) >= 64 {
				s.err = "最多共享 64 个目录"
				return
			}
			name := filepath.Base(dir)
			if name == string(filepath.Separator) {
				name = "文件系统"
			}
			s.config.NextRootID = id + 1
			s.config.Roots = append(s.config.Roots, api.Root{ID: strconv.FormatInt(id, 10), Name: name, Path: dir, ReadOnly: true})
			if err = sharing.Save(a.sharingPath(), s.config); err != nil {
				s.err = err.Error()
			}
		})
	}()
}
func sharingAddresses(listen string) []string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return nil
	}
	if host != "" && host != "0.0.0.0" && host != "::" {
		return []string{net.JoinHostPort(host, port)}
	}
	out := []string{}
	addrs, _ := net.InterfaceAddrs()
	for _, addr := range addrs {
		ip, _, err := net.ParseCIDR(addr.String())
		if err != nil || ip.IsLoopback() || ip.To4() == nil {
			continue
		}
		out = append(out, net.JoinHostPort(ip.String(), port))
	}
	return out
}
func (a *app) connectPeer() {
	s := a.sharing
	if s.connecting {
		return
	}
	name, address, key, err := sharing.ParseInvitation(strings.TrimSpace(s.invitation))
	if err != nil {
		s.err = err.Error()
		return
	}
	s.connecting = true
	s.err = ""
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		defer cancel()
		c := fileindex.New(address, "")
		err := c.Login(ctx, key)
		if err == nil {
			_, err = c.Roots(ctx)
		}
		a.post(func() {
			s.connecting = false
			if a.closed {
				return
			}
			if err != nil {
				s.err = "连接设备失败：" + connectError(err)
				return
			}
			sv := a.store.ServerByAddr(address)
			if sv == nil {
				sv = &config.Server{Addr: address}
				a.store.AddServer(sv)
			}
			sv.Name, sv.Token = name, c.Token
			if sv.Name == "" {
				sv.Name = "MyGo 设备"
			}
			sv.Name = strings.ReplaceAll(sv.Name, "/", "-")
			s.invitation = ""
			a.save()
			a.serverChanged(sv.ID)
		})
	}()
}
func (a *app) sharingCard(c *ui.Context) {
	a.ensureSharing()
	s := a.sharing
	t := c.Theme()
	settingsCard(c, iconLaptop, "设备共享", "在 MyGo 中访问另一台电脑的共享磁盘或目录。当前共享为只读，支持浏览、预览和下载。", func() {
		if s.err != "" {
			notice(c, t.Danger, s.err)
		}
		running := s.process != nil
		locked := running || s.busy || s.picking
		ui.Row(c).Gap(8).Children(func() {
			ui.TextInput(c.Key("sharing-name"), &s.config.Name).Placeholder("本机设备名称").Label("本机设备名称").Disabled(locked).Grow(1)
			ui.TextInput(c.Key("sharing-listen"), &s.config.Listen).Placeholder(":17891").Label("共享监听地址").Disabled(locked).Width(160)
		})
		for i, root := range s.config.Roots {
			ui.Row(c.Key("shared/" + root.ID)).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, iconFolder).FontSize(16)
				ui.Text(c, root.Path).SingleLine().Tooltip(root.Path).Grow(1).Shrink(1)
				ui.Text(c, "只读").TextColor(t.TextMuted).FontSize(12)
				ui.Button(c, "移除").Disabled(locked).OnClick(func() {
					s.config.Roots = append(s.config.Roots[:i], s.config.Roots[i+1:]...)
					if err := sharing.Save(a.sharingPath(), s.config); err != nil {
						s.err = err.Error()
					}
				})
			})
		}
		ui.Row(c).Gap(8).Children(func() {
			ui.Button(c, "添加共享目录…").Disabled(locked).OnClick(a.addSharedDirectory)
			if running {
				ui.Button(c, "停止共享").Disabled(s.busy).OnClick(a.stopSharing)
			} else {
				ui.PrimaryButton(c, "开启共享").Disabled(s.busy || s.picking || len(s.config.Roots) == 0).OnClick(a.startSharing)
			}
			if s.busy {
				ui.Spinner(c).FontSize(14)
			}
			ui.Button(c, "重置访问码").Disabled(locked).OnClick(func() {
				s.config.Key = rand.Text() + rand.Text()
				if err := sharing.Save(a.sharingPath(), s.config); err != nil {
					s.err = err.Error()
				}
			})
		})
		if running {
			ui.Text(c, "共享已开启。对方在“设备共享”中粘贴连接邀请即可访问；停止共享会中断远程读取。").FontSize(12).TextColor(t.TextMuted)
			addresses := sharingAddresses(s.process.Ready.Address)
			if len(addresses) == 0 {
				ui.Text(c, "未找到局域网地址，请检查网络连接。").TextColor(t.Warning)
			}
			for _, addr := range addresses {
				ui.Row(c.Key("invite/" + addr)).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Text(c, addr).Grow(1)
					ui.Button(c, "复制连接邀请").OnClick(func() {
						invite, err := s.config.Invitation(addr)
						if err != nil {
							s.err = err.Error()
							return
						}
						c.Services().WriteClipboard(invite)
					})
				})
			}
		} else {
			ui.Text(c, "仅共享选中的目录；每次启动后手动开启。退出 MyGo 后共享停止。").FontSize(12).TextColor(t.TextMuted)
		}
		ui.Text(c, "连接其他 MyGo").Bold()
		ui.Row(c).Gap(8).Children(func() {
			ui.TextInput(c.Key("peer-invitation"), &s.invitation).Placeholder("粘贴对方复制的 mygo:// 连接邀请").Label("设备连接邀请").Grow(1).Disabled(s.connecting)
			ui.Button(c, "连接设备").Disabled(s.connecting || strings.TrimSpace(s.invitation) == "").OnClick(a.connectPeer)
		})
		ui.Text(c, fmt.Sprintf("连接后的设备显示在侧边栏。连接邀请包含访问码，只交给允许访问的人。最多共享 %d 个目录。", 64)).FontSize(12).TextColor(t.TextMuted)
	})
}

func deviceAddressLabel(address string) string {
	if fileindex.IsPeer(address) {
		if u, err := url.Parse(address); err == nil {
			return u.Host
		}
	}
	return address
}
