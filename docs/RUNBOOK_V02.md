# v0.2 操作手册

## 服务器与身份

v0.1 的服务器初始化、注册、证书信任步骤继续有效，见 `RUNBOOK.md` 的命令。v0.2 增加媒体 UDP 中继配置 `media_relay_listen` 和对外地址；实际 JSON 键以 `internal/server/config.go` 为准。请勿将旧手册的功能状态当作 v0.2 状态。

默认部署必须 HTTPS，服务器私钥只留服务器。两端各自初始化/注册并通过可信渠道核对对方 `public-identity.json` 的完整指纹。新设备没有默认可信公钥。

## 图形客户端

将完整 Windows 构建目录放在一起，启动 `RemoteDesk.exe`。首次进入“初始化 / 注册”，填写自己的 HTTPS 服务器、CA 证书和注册令牌文件。导出公钥，可信核对后在另一端导入。用“设置共享权限”单独授权桌面、键鼠、剪贴板、音频和文件。

在被控端“本机托管设置”选媒体程序和独立共享目录，然后启动当前用户主机。控制端输入设备 ID，选择分辨率/FPS/显示器后连接。双方能力取交集；仅有桌面权限不能自动获得键鼠、音频或文件权限。

视频画布为嵌入的原生 D3D11 窗口。显示的是实际渲染 FPS 和各阶段提交耗时，不是光子到光子延迟。软件默认要求可用硬编和 D3D11 硬解，失败应检查日志而不是跳过安全检查。

SSH 通过本地回环隧道调用 OpenSSH 并在 ConPTY 终端展示。第一次需要核对 SSH 主机指纹；设备公钥与 SSH 用户公钥不是同一种凭据。被控端 sshd 仅在回环监听，不开放公网 22。

文件页可列目录、上传文件/文件夹、下载单文件、取消/继续以及明确覆盖；默认 1 MiB/s。共享目录请独立创建，不放在公共可写目录或系统目录；下载目录也是当前用户私人目录。

## 后台与数据库

`apps/admin` 执行 `npm install && npm run build`，服务器 `admin_directory` 指向构建后的 dist。默认仍可使用旧内置只读管理页。新后台需要管理角色认证，设备禁用不等于删除本地密钥。节点页面记录节点元数据，目前不执行多节点调度。

`services/persistence` 是独立 Go module：设置 `RD_POSTGRES_URL` 和 `RD_REDIS_URL` 后 `go test ./...`；`go build ./cmd/remote-server-pg`。非回环数据库/Redis默认要求 TLS；封闭容器网可明确 `RD_ALLOW_PRIVATE_PLAINTEXT_BACKENDS=1`。不要把这个开关用于公网数据库。

`deploy/compose.yml` 不对宿主机发布数据库/Redis 端口。运行前配置强密码、正确 URL 编码的 DSN 和已初始化的服务器 state 挂载目录；容器运行 UID 65532，挂载目录需匹配读写权限。备份 PostgreSQL 和设备/服务端配置。

## Windows 服务

身份/SSH Service 与当前用户的桌面主机不是同一个运行模式。SYSTEM 不会执行任意媒体配置。当前未实现 WTS Session broker，登录前/UAC安全桌面不支持；不要通过关闭 UAC 规避。

同一设备状态目录不要同时启动多个身份 Agent，否则在线状态会互相覆盖。当前 Qt 只防止同一用户多开 GUI；服务使用专门设备身份目录。完整的全进程状态目录互斥仍需补齐。

## 更新

`release-sign` 的签名私钥只在可信离线发布环境使用；客户端固定发布公钥。`remote-updater` 检查 HTTPS 清单、签名、版本序列、过期时间和 ZIP 安全后只暂存包。`activated:false` 表示尚未安装。当前没有自动替换运行中服务或失败回滚，不要把暂存当作更新已生效。
