# RemoteDesk

Windows P2P 远程桌面与 SSH 一体化工程。

本仓库正在接收现有 v0.1 工程及 v0.2 媒体、客户端、传输和管理扩展。目标视频链路为 DXGI → GPU H.264 编码 → 加密 QUIC → GPU 解码 → D3D11 显示。

**开发状态说明：编译成功、协议测试通过和真实 Windows/GPU 性能验收是不同状态。在提供实机测试记录之前，不宣称已达到 1080p60、ToDesk 级性能或完整商业交付。**

代码上传后，以 `docs/STATUS.md`、`docs/TEST_REPORT.md` 和构建工作流的实际结果为准。不要将运行时私钥、令牌、state 目录或生产配置提交到仓库。
