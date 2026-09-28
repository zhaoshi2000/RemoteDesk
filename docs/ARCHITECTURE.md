# RemoteDesk 技术架构与本次边界

本文件依据用户上传的《粘贴的文本 (1)(1).txt》，特别是第 38 节的十项架构要求编写。
本次交付是可验证的工程增量，不代表原规格全部完成。当前运行环境：Linux x86_64、Go 1.23.2、C++ 编译器；没有 Windows SDK、Qt、Rust，构建环境无法解析外网域名。

## 1. 最终技术架构

- 高频桌面客户端：Qt 6 + Rust 核心；C++ 封装 Windows DXGI/D3D11 与厂商编解码 SDK。
- 最终媒体传输：Quinn/rustls QUIC；可靠流承载控制、键盘、SSH、文件；DATAGRAM 承载允许丢弃的视频分片、鼠标位置和音频。按能力协商，不能把 QUIC 可靠流直接当作低延迟视频的默认实现。
- API/信令/中继控制面：Go。生产持久化目标 PostgreSQL，在线状态/限流目标 Redis。
- 管理后台目标 Vue 3 + TypeScript + Vite + Element Plus。
- 第一版实际实现：Go 标准库控制面、签名请求、原子文件存储、内置只读管理页、UDP STUN/双向探测、独立 TLS 1.3 SSH 连接及加密中继、Windows Service 适配器。没有用 TCP/JPEG 代替媒体链路。
- 为了在现有环境真实编译、运行和测试，本次没有伪造 Rust/Qt 工具链或未经安装的依赖。Go 隧道是独立可用的 SSH 增量，未来可替换传输适配层；不是已完成的 QUIC 核心。

## 2. 模块关系

`remote-agent`（身份/在线/授权/隧道）→ HTTPS `remote-server`（注册/在线/候选/会话/只读后台）。

`remote-agent A` ↔ 独立 TCP TLS 1.3 直连 ↔ `remote-agent B` → B 上的 `127.0.0.1:22`。

无法 TCP 直连时，两端分别外连同一 HTTPS 服务端中继接口，在两条外层 TLS 之间透传**端到端 TLS 密文**。服务器没有设备私钥；本地公钥信任列表是最终授权依据。

UDP STUN 与带签名的打洞探测复用同一个本地 UDP socket。此阶段探测成功只表示候选地址可达，不等于已建立 QUIC 媒体会话，也不等于 TCP SSH 可以穿过该 NAT。

## 3. 数据流

最终桌面：DXGI → D3D11 纹理 → GPU 色彩转换 → NVENC/QSV/AMF H.264 → QUIC DATAGRAM → 硬件解码 → D3D11。本次只提供 DXGI GPU 采集诊断源码，不声称视频端到端已接通。

当前 SSH：本地回环 TCP listener → 端到端双向认证 TLS 1.3 → 直连或中继 → 被控端固定回环 SSH 端口。SSH 原有主机密钥校验和 Windows 用户认证仍然必须完成。

## 4. P2P 建连流程

1. 安装端生成 Ed25519 身份，注册时证明持有私钥；控制面必须有 TLS 和注册令牌。
2. 两端各自完成 UDP STUN，提交签名候选清单；服务器只协调，不作为设备授权根。
3. 双方通过线下/可信渠道交换公钥，再在本机添加授权。
4. 控制端发起有时间窗、有随机数、有双方 ID 的签名 intent。目标端校验本机 ACL 后回应。
5. 两端向对方候选发送有签名的 UDP 探测，复用先前 STUN socket。
6. 最终 QUIC 版本在相同 UDP 端点建立双向认证连接；打洞超时转中继。此步骤本次尚未接线。
7. 当前 SSH 独立尝试已签名的 TCP LAN 候选，失败后走端到端 TLS 中继。不将这个结果冒充 UDP P2P 媒体连接。

不能承诺任意对称 NAT/企业防火墙均能直连。单台公网服务器可合并部署 API、信令、STUN、中继，但不能消除带宽/CPU/公网端口限制。

## 5. Windows Service 架构

服务负责身份、在线心跳、ACL、连接及 SSH；用户桌面进程负责采集/输入/渲染。未来使用 ACL 限制的 Named Pipe 按用户会话分离，拒绝来自非授权 SID 的 IPC。

当前包含 SCM 生命周期适配源码，可交叉编译，不代表已在 Windows 实机验证。当前服务不启动交互式捕获，不声称支持锁屏、UAC 安全桌面或 Ctrl+Alt+Delete。SendInput 受 UIPI 限制；这些能力必须单独验证，禁止关闭 UAC 规避。

## 6. SSH Tunnel 架构

每条 SSH TCP 连接拥有独立端到端 TLS 连接，TLS 两端校验本地预先授权的设备公钥。控制服务器签发一次性、短时、中继槽位票据，但这个票据不能替代被控设备本地授权。

被控端只可转发到本机配置的回环端口，远程客户端不能指定任意目标。监听转发的控制端只接受回环绑定。首次连接仍需按正常 SSH 方式确认 OpenSSH 主机密钥、使用 Windows 用户/密钥登录。

安装脚本只为**新安装的 OpenSSH 配置**设置 loopback 监听，不默认改写既有远程 SSH。已有 sshd 配置不符合要求时停止 SSH 安装步骤，避免把用户现有管理通道切断。

## 7. 安全方案

- HTTPS 服务端 CA/证书校验；没有生产模式的全局跳过证书验证选项。
- Ed25519 签名请求：方法、路径、正文哈希、时间戳、随机数；服务端防重放缓存有界且按窗口清理。
- 设备私钥只在本机。Windows 采用 DPAPI machine protection + 独立文件 ACL；Linux 开发环境使用 0700 目录/0600 私钥。
- 端到端 TLS 1.3 强制比较已在本机固定的设备公钥；既验证 TLS CertificateVerify，也校验自签名证书/有效期。局域网直连与中继使用同一规则。
- 无默认无人值守口令、无服务器代授权、无公网 SSH、防止枚举设备、限制请求正文/会话数量/队列、票据一次性。
- 首次授权是本地显式导入的公钥文件；6 位验证码/PAKE 尚未实现，不能用服务器返回公钥悄悄自动信任。
- 服务器配置/证书、设备私钥、注册令牌和管理令牌都不打进交付压缩包；日志不输出密钥或票据。
- 尚需生产安全审计、证书/密钥轮换、分布式防重放、权限撤销在存量长连接上的即时生效、密码登录 PAKE、签名更新等。

## 8. 项目目录

`cmd/` 可执行入口；`internal/` 协议、身份、存储、API、STUN、代理、隧道、Windows Service；`internal/server/static/` 内嵌管理页；`native/` DXGI 源码和可测试调度/码率模块；`scripts/` 初始化/构建/安装；`installer/` 安装器源文件；`tests/` 端到端验证；`docs/` 架构、安全、状态、测试结果；`deploy/` 部署模板。

## 9. 第一阶段计划

按照原要求推进可验证增量：构建基础 → 注册/在线/授权 → UDP 候选与双向可达探测 → DXGI 采集 → GPU 编码 → QUIC 媒体 → 硬解/渲染 → 键鼠 → 自适应 → 中继切换 → 剪贴板/文件 → OpenSSH → 集成终端 → 安装器 → 正式后台 → 性能实测。

SSH 加密隧道提前作为网络安全验收载体，不表示阶段 4–12 已完成。1080p60/15–30ms 是待测目标，不是本次测试结果。直连和中继间的无缝切换/会话恢复本次不保证。

## 10. 依赖及选择理由

本次 Go 运行部分只有 Go 标准库：`crypto/tls`、`crypto/ed25519`、`net/http`、`net`、`encoding/json` 等，因此可以离线构建与审计。Go 1.23.2 是本环境实际工具链，不代表推荐生产部署版本；生产发布必须使用受支持工具链重编译并扫描。

原生诊断：C++20、Windows SDK D3D11/DXGI、WRL COM；采集返回 GPU 纹理，不做 GDI 截图。C++ 无平台调度/自适应模块可在当前环境编译。

后续选型：Qt6（动态链接及许可证审查）；Rust稳定工具链、Quinn/rustls；NVENC SDK/QSV/AMF；H.264/H.265/AV1相关 SDK/专利/分发许可单独审查；Opus；PostgreSQL；Redis；Vue3/Vite。未下载/未编译的依赖不伪造 lock 文件或声称已集成。

## 官方资料（实现时核对）

- https://learn.microsoft.com/en-us/windows/win32/direct3ddxgi/desktop-dup-api
- https://learn.microsoft.com/en-us/windows/win32/api/winuser/nf-winuser-sendinput
- https://learn.microsoft.com/en-us/windows-server/administration/openssh/openssh-server-configuration
- https://quinn-rs.github.io/quinn/quinn.html
- https://quic-go.net/docs/quic/datagrams/
- https://doc.qt.io/qt-6/licensing.html
