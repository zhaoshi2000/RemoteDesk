# 运行与部署手册

## 0. 先确认当前版本能力

当前能做：通过设备 ID 查询连接、显式授权、公钥校验、SSH TCP 隧道、直连失败后中继、UDP 双向可达测试、只读后台。

当前不能做：远程桌面显示、GPU 编解码、1080p60 性能验收、内嵌图形终端、无缝迁移。下面的 `ssh` 命令调用系统 OpenSSH 客户端，不是 Qt 内嵌终端。

正常使用流程是：初始化服务器 → 两台设备各自初始化/注册 → 通过可信渠道核对并交换公钥 → 双方添加授权 → 双方运行 Agent → SSH/UDP 探测。

## 1. Linux 服务器

为隔离验证准备一台测试服务器。正式放到公网前完成安全审查；不要把含私钥/令牌的 state 目录上传到代码仓库。

下面以 `remote.example.com` 为部署域名；替换成实际域名，并保证 DNS 已指向服务器。这个参数会写入 TLS 证书 SAN，不能填一个与客户端连接地址无关的名字。

```bash
chmod +x dist/linux-amd64/remote-server
./dist/linux-amd64/remote-server init \
  --dir "$PWD/state/server" \
  --listen 0.0.0.0:8443 \
  --stun 0.0.0.0:3478 \
  --hosts remote.example.com

./dist/linux-amd64/remote-server run --config "$PWD/state/server/server.json"
```

使用公网 IP 时，`--hosts` 写实际公网 IP，客户端也使用同一个 IP。仅在同一台机器验证时可使用 `--hosts localhost,127.0.0.1` 和默认回环监听。

监听端口：HTTPS/API/SSH 中继共用 `8443/TCP`；STUN 使用 `3478/UDP`。这是 RemoteDesk 的端口，不是要求对外开放 SSH 22。

服务端生成 `server.crt`、`server.key`、`server.json`、`enrollment.token`、`admin.token`。客户端只复制公开证书 `server.crt`；注册令牌通过可信私密渠道临时提供；管理令牌只给管理员。不要把 server.key 发给客户端。

浏览器打开 `https://remote.example.com:8443/`。自签名证书需要由管理员按组织流程建立信任，不提供跳过校验开关；填写 `admin.token` 内容查看真实设备/在线/会话数据。

当前服务端必须单进程运行，使用文件持久化设备信息。不要用多个副本共享一个 JSON 文件。事件、在线状态、防重放缓存和中继票据是进程内短时状态。

## 2. Windows A、B 各自初始化

在每台 Windows 电脑新建独立工作目录，将 `remote-agent.exe` 放进去，再放入服务器公开证书 `server.crt` 和暂时使用的 `enrollment.token`。

准备作为服务运行时，从管理员 PowerShell 初始化到 `%ProgramData%\RemoteDesk\agent`，不要从其他机器复制私钥目录。

电脑 A：

```powershell
$State = "$env:ProgramData\RemoteDesk\agent"
.\remote-agent.exe init --state $State --server https://remote.example.com:8443 --ca .\server.crt --stun remote.example.com:3478 --name "办公室-A"
.\remote-agent.exe register --state $State --token-file .\enrollment.token
.\remote-agent.exe identity --state $State
```

电脑 B 同样执行，`--name` 改为它自己的名称。程序显示的是实际生成的 **12 位设备 ID**，不是需求原文中的示例 ID。

每台机器生成 `public-identity.json`（可分享）、`identity.key`（不可分享）、`trusted.json`、`agent.json` 和服务器公开证书副本。Windows 私钥是该机器上的 DPAPI machine 保护格式，目录 DACL 仅授权初始化用户、SYSTEM、Administrators。

测试账户普通运行时也可用当前用户自己的新目录。Machine DPAPI 不能代替文件 ACL；不要把 state 放进共享盘或其他用户可写的目录。

## 3. 交换公钥并明确授权

通过可信渠道核对 `public-identity.json` 中完整 `fingerprint`。在 A 上导入 B 的公钥文件，在 B 上导入 A 的公钥文件；不要从不可信的服务器页面直接下载一个公钥并自动信任。

A：

```powershell
.\remote-agent.exe trust --state $State --peer-file .\B-public-identity.json
```

B：

```powershell
.\remote-agent.exe trust --state $State --peer-file .\A-public-identity.json
```

默认 `trust` 同时授权 SSH 和 UDP 探测；可用 `--ssh=false` 或 `--probe=false` 关闭某一能力。首次安装的信任表为空，没有默认无人值守口令。

## 4. 运行两端 Agent

每台电脑各开一个 PowerShell：

```powershell
.\remote-agent.exe run --state $State
```

看到 `agent online`，并在后台确认两端在线。服务端与客户端要保持系统时间同步；签名请求只允许有限时钟偏差。

客户端 Agent 的随机 TCP 监听端口只接受双向 TLS 与本地 ACL 授权，不是裸 SSH。默认 UDP 探测和服务端连接也由 Agent 维持。

## 5. 安装/检测 OpenSSH（在被控电脑上）

第一次安装可使用：

```powershell
powershell -NoProfile -File .\scripts\Install-OpenSSH.ps1
```

该脚本需要管理员权限和 Windows 可选组件来源可用。它会检查能力、安装官方 OpenSSH Server、禁用新安装默认创建的 SSH 入站规则、限制 sshd 只监听 127.0.0.1/::1:22、校验配置再启动服务。

**既有 OpenSSH 不符合回环限定时，脚本报错并保持其原配置不变**，不会为了 RemoteDesk 强行切断已有运维通道。此时需要在测试机或单独规划的 SSH 实例上验证。没有运行脚本成功之前，不会报告 OpenSSH 已安装。

控制电脑还必须具有 PATH 中可用的 OpenSSH Client；本脚本只处理被控电脑的 OpenSSH Server。

该版本使用 Windows/OpenSSH 公钥认证，没有创建公共默认用户或默认密码。用自己的 Windows/OpenSSH 账号和已授权 SSH 密钥。RemoteDesk 的 Ed25519 设备身份与 SSH 用户公钥是不同层的凭据。

Windows PowerShell 执行策略受组织策略约束。本项目不关闭安全软件、不全局修改执行策略；企业分发应使用签名脚本或既有可信软件部署流程。

## 6. 按设备 ID 打开 SSH

在 A 上另开 PowerShell，替换下面的 `B设备ID` 和 `Windows用户名`：

```powershell
.\remote-agent.exe ssh --state $State --peer B设备ID --user Windows用户名
```

程序监听随机本地回环端口，尝试签名候选地址的 TCP 直连；不可达时自动发起中继连接。随后启动系统 `ssh`，以 `remotedesk-B设备ID` 为 HostKeyAlias，首次仍需核对 OpenSSH 主机密钥。

强制验证中继：

```powershell
.\remote-agent.exe ssh --state $State --peer B设备ID --user Windows用户名 --relay-only
```

给其他终端工具使用固定回环端口：

```powershell
.\remote-agent.exe tunnel --state $State --peer B设备ID --listen 127.0.0.1:22345
ssh -o HostKeyAlias=remotedesk-B设备ID -p 22345 Windows用户名@127.0.0.1
```

普通 `tunnel` 模式的 SSH 客户端负责自己的 known_hosts 配置；内置 `ssh` 子命令使用 state 下的独立 known_hosts 文件。

日志显示 `Direct TLS/TCP (not UDP QUIC)` 或 `Relay / end-to-end TLS 1.3`。请不要把第一个标签理解成已完成 UDP QUIC 视频穿透。

## 7. 测试 UDP 双向可达

```powershell
.\remote-agent.exe probe --state $State --peer B设备ID
```

需要双方 Agent 在线且互相授权。双方都返回可达表示有签名的 UDP ping/pong 已通过，不能据此宣称已实现媒体连接、NAT 全类型兼容或任意网络百分百穿透。

当前测试报告只证明同机回环环境的协议/并发逻辑；不同运营商、对称 NAT、IPv6-only、UDP 被封锁等条件尚未进行实网验证。

## 8. Windows 服务和卸载

先完成上述前台运行验证，再停止前台 Agent，管理员执行：

```powershell
powershell -NoProfile -File .\scripts\Install-AgentService.ps1 -AgentExe .\remote-agent.exe -StateDirectory $State -AllowPrivateLAN
```

安装脚本会将服务程序复制到 `%ProgramFiles%\RemoteDesk\ServiceBin`，限制为管理员/SYSTEM 可写，避免让 LocalSystem 服务从 Downloads 等用户可写路径启动。若 ServiceBin 已有内容，则拒绝覆盖；这不是自动升级脚本。

可加 `-InstallOpenSSH` 运行前面的可选 SSH 安装步骤。`-AllowPrivateLAN` 只创建 RemoteDesk 程序自身的私有/域网络 TCP、UDP 规则，不开放公网 SSH。

```powershell
Get-Service RemoteDeskAgent
Stop-Service RemoteDeskAgent
Start-Service RemoteDeskAgent
```

卸载服务：

```powershell
powershell -NoProfile -File .\scripts\Uninstall-AgentService.ps1
```

卸载只删除本项目服务和本项目命名的防火墙规则，保留设备密钥、配置、受保护程序副本及 OpenSSH。重新安装前由管理员明确处理保留的 ServiceBin，脚本不自动删除它。服务模式目前不处理用户会话捕获、UAC、安全桌面、Ctrl+Alt+Delete；Service/DPAPI 安装行为尚未在本交付环境的 Windows 实机上测试。

## 9. 撤销授权

```powershell
.\remote-agent.exe untrust --state $State --peer 对方设备ID
```

新连接立即按新的 ACL 拒绝。当前版本尚未实现从 ACL 更新主动断开所有存量 TLS 会话；撤销后重启 Agent 可关闭存量连接。

## 10. DXGI 诊断

安装 MSVC 与 Windows SDK 后：

```powershell
powershell -File scripts/build-windows.ps1 -WithNative
.\build\native\Release\capture-probe.exe --seconds 10 --adapter 0 --output 0
```

在真实交互桌面内运行，移动窗口或播放视频后观察采集更新。静止桌面可能更新极少，这不是完整视频帧率测试。程序输出 GPU 适配器/输出、纹理尺寸、实际取得帧数量、脏区域、鼠标形状变更，不编码、不传视频、不计算端到端延迟。

## 11. 故障定位

`unknown device`：先 register。`server must be HTTPS`/证书错误：检查域名/IP 与 SAN、CA 文件。`target offline`：被控 Agent 未运行或心跳失败。`not locally authorized`：缺少相应公钥/能力授权。`remote SSH endpoint unavailable`：sshd 尚未监听配置的回环端口。`UDP probe did not confirm both directions`：不能据此认定 SSH 中继也不可用。

当前日志输出到标准错误，systemd 可交给 journal 管理。Windows SCM 模式尚缺持久事件日志/日志轮转；排障时停止服务，在受控终端前台 `run`。不要开启记录 SSH 内容、密钥或验证码的调试日志。
