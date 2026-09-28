# RemoteDesk 0.3 — 两个交付包

## 下载和安装只分服务端、客户端

正式发布只允许以下两个文件。源代码模块、测试可执行程序、编解码 DLL、终端网页和 Agent 都是内部组成，不是分别安装的产品。

| 文件 | 安装位置 | 内含 |
|---|---|---|
| RemoteDesk-Server-linux-amd64.tar.gz | 一台 Linux x86_64 公网服务器 | Go 服务、内嵌 Vue Admin、API、设备注册、信令、STUN/UDP 媒体中继、SSH 密文中继、安装脚本、systemd 服务、校验和 |
| RemoteDeskSetup.exe | Windows 10/11 x64 控制/被控电脑 | Qt 主界面、Agent、媒体工作进程、Rust QUIC、SSH 终端资源、运行库、OpenSSH 检测安装脚本 |

这是打包约定，不代表本机或 CI 已通过 Windows GPU 性能验收。完整发布检查会拒绝缺失任一文件，也不会用命令行 Agent 冒充图形客户端安装包。客户端内部存在多个进程，但用户只安装一次。

## 服务端安装

前提：支持 systemd 的 Linux amd64，域名已经解析到本机，或直接使用公网 IPv4。安装脚本需要管理员权限；它不关闭防火墙，不修改已有 SSH 服务，也不覆盖已有 RemoteDesk 密钥或配置。

```bash
tar -xzf RemoteDesk-Server-linux-amd64.tar.gz
cd RemoteDesk-Server
sudo bash install.sh --host remote.example.com
```

替换 remote.example.com 为实际域名或公网 IP。云安全组及主机防火墙放行：8443/TCP（HTTPS、API、后台、SSH 加密中继）、3478/UDP（STUN、媒体密文中继共用）。不需要为本系统对公网开放 SSH 22。

部署后打开：`https://remote.example.com:8443/admin/`。

默认管理员名 `admin`，无统一默认密码。读取初始化时随机生成的私密密码：

```bash
sudo cat /var/lib/remotedesk/server/admin.token
sudo systemctl status remotedesk-server
sudo journalctl -u remotedesk-server -f
```

密码不打印在部署日志、不写浏览器本地存储。其他管理用户可在后台创建，用各自独立的访问令牌登录；支持管理员、会话操作员、只读用户。用户令牌只保存哈希，创建时仅返回一次。

默认生成自签名 TLS 证书。正式使用应按组织流程建立信任，或在私有 `server.json` 内替换为受信任证书/私钥路径并重启服务；不能通过关闭证书校验处理。启动日志中的“就绪”只有在监听端口和证书加载成功后出现。

服务二进制在 `/opt/remotedesk/server/bin`，持久状态在 `/var/lib/remotedesk/server`。运行账户为专用非 root 账户 remotedesk。已有安装会使脚本停止，避免静默覆盖密钥；升级应备份私密状态并采用明确的替换流程，不要反复把初始化安装当升级。

## 后台页面

总览、服务器节点、设备管理、会话与中继、性能监控、用户与权限、操作审计、签名版本、系统设置、安全中心。Vue 3 + TypeScript + Vite + Element Plus；前端编译资源嵌入 Go 二进制，通过同一 HTTPS 地址提供。服务器无需 Node.js、npm、另一个前端服务、Nginx 或数据库才能运行默认单机模式。

节点自动取当前运行的服务器。5 秒采样及刷新，展示心跳、运行时间、监听状态、CPU、内存、磁盘、进程驻留内存、网卡吞吐和最近一小时曲线。第一次需要两次采样的 CPU/网速显示“未采集”。请求失败立即显示失联，超过20秒未取得新数据标为过期；旧数据不会继续作为绿色在线状态。

服务自身不能在断电后继续提供网页。已经打开的页面会标为失联；此时新打开网页可能无法访问。要在主机彻底宕机时仍有可访问的监控页面，需要独立外部监控。这版没有把“本机正在运行”等同于“所有运营商均能连接”。

设备支持名称/ID检索、状态筛选、分组、管理别名、备注、中心访问禁用、分页和 CSV 导出。禁用关闭中心中继并拒绝新中心访问，不能替代设备端本地公钥信任撤销，也不能保证立即关闭独立直连。

网卡指标为 Linux 主机可见计数，容器环境可能反映宿主机范围；不是精确 cgroup 配额计量。中继只累计通过鉴权的加密载荷，不解码视频；直连 P2P 会话数量/字节数未接入客户端遥测，因此明确显示“未上报”，不使用模拟数据。

版本页面保留原有签名清单接口。先配置更新验签公钥，离线生成 manifest/signature 后发布；签名私钥不得上传。当前更新器是校验/安全暂存，不是已经完成自动安装回滚。

默认状态使用原子 JSON。已有 PostgreSQL/Redis 扩展源码保留；默认这一份服务端包不强迫用户再安装数据库包。当前信令仍按单个服务进程工作；新增控制台分组/节点展示配置保存在本机状态文件，不声称已实现分布式管理。

## 编译与发布

Linux 服务端：
```bash
bash scripts/release/build-server.sh
```

Windows（PowerShell 7，Go、Rust、Node、MSVC、CMake、Qt 6.8+ WebEngine/WebChannel、vcpkg、Inno Setup）：
```powershell
./scripts/build-full-windows.ps1 -Installer
```

总发布目录 `release/` 中仅允许服务端 tar.gz 和客户端 Setup.exe。`verify-release.py` 检查文件数、格式和服务端内容。发布页面不附加动态库包、测试包或未验证的占位安装器。源码仍按模块维护，与产品下载数量无关。

Windows 完整视频、无人值守跨用户会话、安全桌面及1080p60性能仍需实机验证；编译成功不等于这些能力达标。
