package main

import (
	"fmt"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/updater"
	"github.com/egoist/mygo/plugins/updater/native"
	"github.com/egoist/mygo/ui"
)

func (a *app) useUpdater() {
	mygo.Use(native.New(updater.Options{Language: "zh-Hans"}))
	a.updatesReady = true
}

func (a *app) updatesCard(c *ui.Context) {
	if !a.updatesReady {
		return
	}
	settingsCard(c, iconServer, "软件更新", "客户端与本机后台一起更新，重启后生效。", func() {
		ui.Text(c, fmt.Sprintf("当前版本：%s", mygo.App.Version()))
		if !mygo.Updater.Enabled() {
			ui.Text(c, "当前构建未启用应用内更新，或由系统包管理器管理。").FontSize(12)
		}
		ui.Button(c, "检查更新…").OnClick(updater.CheckForUpdates)
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "自动检查更新").Grow(1)
			automatic := updater.AutomaticChecks()
			if ui.Switch(c.Key("automatic-update-checks"), &automatic).Label("自动检查更新").Changed() {
				updater.SetAutomaticChecks(automatic)
			}
		})
		ui.Text(c, "有传输或备份任务时，重启仍需先确认结束任务。").FontSize(12)
	})
}
