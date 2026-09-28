# RemoteDesk

Windows P2P 远程桌面 + SSH 工程，v0.2 开发增量。代码在 v0.1 设备注册、密钥和 SSH 隧道基础上扩展，不依赖第三方远控软件。

**这是工程开发版本，不是已经通过全部实机验收的商业成品。** 是否完成编译看 GitHub Actions；GPU 性能、跨运营商网络、安装卸载和交互行为必须以真实 Windows 10/11 机器的测试记录为准。没有报告不代表默认通过。

## 实际模块

| 目录 | 实现 |
|---|---|
| `cmd/remote-agent`, `internal/agent` | 身份、设备授权、在线、SSH/文件连接、媒体会话 |
| `cmd/remote-server`, `internal/server` | HTTPS API、信令、中继、管理权限和发布接口 |
| `crates/remote-quic` | Rust/Quinn + rustls，双向 Ed25519 公钥固定校验，有界通道和视频帧取消 |
| `native` | DXGI → D3D11 NV12 → FFmpeg 硬件 H.264 → QUIC → D3D11 硬解/显示 |
| `apps/client` | Qt 6 设备管理、远程桌面嵌入窗口、文件和 SSH 标签页 |
| `apps/terminal` | 本地 xterm.js + Qt WebChannel + Windows ConPTY，运行真正的 OpenSSH |
| `apps/admin` | Vue 3 / TypeScript / Element Plus 管理界面，调用真实管理 API |
| `services/persistence` | PostgreSQL 注册/管理存储、Redis 在线 TTL 和防重放 |
| `internal/update`, `cmd/remote-updater` | Ed25519 发布验签、拒绝过期/降级、安全解包至暂存目录 |
| `installer`, `scripts` | 完整构建、Inno Setup 打包、OpenSSH 和身份服务安装 |

视频不是 JPEG/WebSocket。硬件编码后端由 FFmpeg 调用 **NVENC / QSV / AMF**；无可用硬件时默认明确报错，不谎报硬编成功。OpenSSL 3.5 QUIC 作为可测试的备选实现保留。普通高优先级消息使用可靠独立流，视频按帧使用可取消的独立 QUIC 单向流，**当前不是 QUIC DATAGRAM 实现**。

## 构建

基础服务/协议测试无需 GUI 依赖：

```sh
go test -count=1 ./...
go test -race -count=1 ./...
go vet ./...
cmake -S native -B build/native
cmake --build build/native --config Release
ctest --test-dir build/native -C Release --output-on-failure
cargo test --workspace --all-targets
```

Windows 完整构建需要 MSVC 2022、Windows SDK、Rust、Go、Node 22、Qt 6.8+（Widgets/WebEngine/WebChannel）、vcpkg（FFmpeg/Opus）及可选 Inno Setup。依赖功能见 `vcpkg.json`。

```powershell
pwsh -File scripts/build-full-windows.ps1 -VcpkgRoot C:\vcpkg -Installer
```

输出在 `dist/windows-amd64`，安装程序在 `dist/installer`。GitHub 工作流提供 Windows/Linux/Rust、前端和 PostgreSQL/Redis 分项验证。CI 编译不替代 GPU 性能测试。

## 上手

先按 [运行手册](docs/RUNBOOK_V02.md) 初始化服务器与两端设备；通过可信渠道核对公钥并分别授权桌面、键鼠、文件、剪贴板和音频。服务端不能仅凭设备 ID 获得控制权限。不要提交或传播 `state`、私钥、注册/管理令牌。

Qt 客户端可初始化/注册设备、添加可信公钥、启动当前用户主机并按设备 ID 连接桌面、SSH 和文件。视频工作进程通过受控进程输入接收会话配置，私钥不放命令行或临时文件。桌面主机会显示连接提示；关闭提示窗口、撤销授权或结束父 Agent 将断开会话并释放按键。

## 不能忽略的边界

**尚未实现完整 WTS 用户会话代理、登录前桌面、UAC 安全桌面、Ctrl+Alt+Delete、HDR、无损文件夹下载、更新自动激活/回滚和多节点高可用。** 身份/SSH Windows Service 与交互桌面主机分离；SYSTEM 服务不会执行用户可写目录中的媒体程序。签名更新目前验证和暂存，不自动覆盖运行中的程序。

1080p60、2K/4K/120FPS、15–30 ms 延迟都是验收目标，不能从源码或本地回环测试推断已达到。更多状态见 [状态表](docs/STATUS.md)、[测试报告](docs/TEST_REPORT.md)、[安全说明](docs/SECURITY_V02.md)。

## 许可证与再分发

第三方组件保留各自许可证。Qt/FFmpeg/Opus/OpenSSH 的许可、动态链接和二进制再分发条件需逐项遵守。工程不附商业许可证授权，也不自动替用户选择开源许可证。未签名构建仅用于受控测试，不建议直接广泛部署。
