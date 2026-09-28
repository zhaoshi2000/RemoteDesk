# v0.2 测试报告

## 当前容器实际执行

环境：Linux amd64，Go 1.23.2、C++20、CMake、OpenSSL 3.5.5；没有 Windows 交互桌面或可用于 DXGI 的 GPU。

- `go test -count=1 ./...`：通过。涵盖媒体协议/授权、中继、文件路径/传输、签名更新、管理认证和原有注册/SSH 回归。原始日志 `v02-tests/go-final.txt`。
- CMake 编译：通过；`realtime-primitives` 与 `openssl-quic-udp` 两个 CTest 项目通过。
- OpenSSL 测试实际建立 UDP 上的 QUIC 双向证书认证，发送 256 KiB 帧和独立输入消息，并验证未授权公钥被拒绝。它不是 GPU 图像测试，也不是 Rust 测试。

## GitHub Actions

Rust 首次编译实际发现新编译器的 `dangerous_implicit_autorefs` 错误；源码改为显式借用，并加强可靠流被截断时的断开处理。后续结果应查对应提交的真实 Actions 日志，不以本文件预告成功。

Windows 原生、Qt、前端、PostgreSQL/Redis 各由独立工作流运行。根目录 Go 测试**不包含** `services/persistence` 嵌套模块，必须单独测试。

## 未实施的验收

真实 Windows/GPU 编码输出、解码画面、键鼠和音频交互；不同 DPI/多显示器/驱动；公网 NAT 与弱网；干净 Windows 安装卸载；Windows sshd 登录；1080p60/2K/4K/120FPS 与端到端光子延迟。构建通过也不能代替这些验收。
