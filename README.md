# RemoteDesk — 两个产品包

发布面只提供 **Linux 服务端部署包** 和 **Windows 客户端安装包**。后台使用 Vue 3 / Element Plus Admin，打包嵌入服务端，不单独安装前端。

- `RemoteDesk-Server-linux-amd64.tar.gz`：服务器端，含管理后台、API、信令、STUN/媒体中继和 SSH 密文中继。
- `RemoteDeskSetup.exe`：客户端，含 Qt 主界面、后台 Agent、媒体进程、Rust QUIC、终端及必要依赖。内部多个组件不是多个产品。

安装、账号密码、端口、在线状态定义和编译流程见 [两个包的部署手册](docs/TWO_PACKAGES.md)。

## 后台

`https://你的域名:8443/admin/`；默认账号 admin，密码为服务器首次初始化生成的私密 admin.token，不存在公开默认密码。节点自动呈现当前运行的服务端。心跳、CPU、内存、磁盘、网速、设备/会话/中继和审计使用真实数据；失联和过期数据不显示为在线。

管理页包括总览、服务器节点、设备分组/备注、会话中继、性能趋势、用户权限、操作审计、签名版本、系统设置和安全中心。直连流量未上报时显示“未上报”，不伪造指标。

## 构建

Linux 运行 `bash scripts/release/build-server.sh`。Windows 运行 `./scripts/build-full-windows.ps1 -Installer`（所需 SDK 见部署手册）。GitHub Actions 的 **Two product packages** 工作流只上传两个产品产物；常规 Rust CI 不上传动态库或测试包。打 v* 标签只会在两端都成功后创建含两个产品文件的预发布。

源码实现、编译、实际运行、GPU 性能验收是不同状态。当前不能仅凭 CI 编译宣称两台 Windows 远控达到 1080p60；无人值守安全桌面和更新安装回滚也不在本次后台打包改造验收范围。原有工程记录保留作为历史，不应把历史计划当作已完成。
