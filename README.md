# FileSync 文件备份

把本机的目录手动备份到 NAS 上的 [fileindex](../fileindex)，支持 Linux 和 Windows。界面使用 [MyGo](https://github.com/egoist/mygo) 的 Native UI 编写（纯 Go，不需要 cgo，也不需要 WebView）。

## 功能

- **多个服务器**：在“服务器…”中添加、编辑、删除 fileindex 服务器（名称 + 地址），登录或退出登录。只保存登录会话，不保存密码；会话过期时预览会提示重新登录。还有任务在用的服务器不能删除。
- **选择备份到的目录**：选择服务器 → 登录 → 选择 fileindex 的索引目录 → 逐级进入子目录，也可以新建文件夹。
- **三种模式**
  - 增量备份：上传新增和修改过的文件，NAS 上多出来的文件保留不动。
  - 完全一致：在增量备份的基础上，把本机没有的文件移到 fileindex 的回收站（默认保留 30 天）。
  - tar 归档：把整个源目录打包成一个归档文件保存到 NAS，每次生成一个新文件（如 `xiaomai-20261010-160500.tar.gz`），不与 NAS 上已有的内容比较。可选 gzip（默认，多核并行压缩）、不压缩或 zstd。请求末尾附带字节数，NAS 核对完整后才以正式文件名保存。
- **同名文件夹**：默认在目标中创建与源目录同名的文件夹（归档模式下是归档里的顶层文件夹）；取消勾选则只备份源目录里的内容。
- **先预览再执行**：先列出要新增、更新、删除、跳过的文件，确认后再开始。
- **排除规则**：glob 语法，例如 `*.tmp`、`node_modules`、`cache/`、`/build/out`。

## 传输方式

不经过 SMB，而是直接和 fileindex 通信：

- NAS 在本地一次性列出目标目录（2 万个文件约 0.1 秒）；
- 要传的目录、文件和符号链接作为**一条标准 tar 流**，在**一个连接、一个请求**中连续发送，磁盘可以顺序写入，小文件也不用逐个往返；
- NAS 边收边解开成普通文件，每写好一个就回一条确认：进度显示的是 NAS 上真正写好的文件，连接中断时也能准确知道哪些已经写好，下次备份会接着传；
- 保留修改时间、可执行权限和**符号链接**；文件属主沿用 fileindex 的设置（Unraid 上为 nobody:users，与 SMB 写入的一致）；
- 写入的文件立即进入 fileindex 的索引，可以马上搜索。

需要 NAS 上的 fileindex 包含 `/api/backup/manifest`、`/api/backup/tar` 和 `/api/backup/file` 接口（`/api/backup/file` 需要支持 `X-Backup-Size` 结尾标记）。

## 安全措施

- 完全一致模式下，如果源目录为空而 NAS 上有文件，拒绝执行（防止源目录未挂载时清空备份）。
- 要删除 NAS 上一半以上的文件时，会再次弹窗确认。
- 删除的文件进入 fileindex 回收站，可以恢复；含有被排除文件的目录会保留。
- NAS 上每个文件先写入临时文件，完整后再改名，不会留下写了一半的文件；传输中被修改而读不完整的文件会被丢弃，下次重传。
- NAS 拒绝写到索引目录之外，包括经由符号链接。
- NAS 上无法读取的目录，其中的内容不会被删除。
- 设备、管道等特殊文件会被跳过。

## 开发

需要 Go 1.27.1 或更高版本。Linux 上运行还需要 GTK 3。

```sh
go run .                 # 运行
go test ./...            # 测试（界面测试不需要窗口，用 fileindex/fitest 模拟服务器）
go tool mygo dev         # 开发模式，修改代码后自动重新加载
go tool mygo build       # 打包
GOOS=windows go build -o filesync.exe .   # 在 Linux 上交叉编译 Windows 版本
```

对真实的 fileindex 测速：

```sh
FILESYNC_E2E_ADDR=192.168.0.235:8090 FILESYNC_E2E_TOKEN=会话 FILESYNC_E2E_SRC=本机目录 go test -run E2E -v ./fileindex
```

设置和日志保存在 `~/.config/filesync/`（Windows 上是 `%AppData%\filesync\`），设置文件只有本人可读。

## 代码结构

```
engine/     比较与计划：扫描本机目录、对比目标、生成计划、执行（Target 接口）
fileindex/  fileindex 客户端和备份目标：目录清单、tar 流上传、tar 归档、回收站删除
            fitest/ 测试用的模拟 fileindex 服务器
config/     服务器、任务和运行记录的读写
app.go      任务列表、任务详情、编辑页面
nas.go      服务器管理、登录、目录浏览对话框
job.go      预览、执行进度、结果页面
runlog.go   每次运行的日志文件
main.go     窗口和系统接口（目录选择对话框、打开文件和网页、防止休眠）
```
