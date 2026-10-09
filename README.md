# RKCAN

运行在 Rockchip（RK3566/RK3568/RK3588/RK3576 等）Linux 板卡上的双路 CAN-FD 网关和 Web 管理界面：

- **CAN → UDP 转发**：两路 CAN/CAN-FD 报文（含 BRS）实时打包转发到上位机，带序号与 CRC。
- **报文发送**：单帧/周期发送，支持标准帧、扩展帧、远程帧、CAN-FD、BRS。
- **实时监控**：按 ID 汇总的报文列表（数据、周期、计数），数据变化高亮。
- **报文回放**：读取 TF 卡 / U 盘 / 内部存储上的 Vector **ASC** 或 **BLF** 文件，实时或 0.1×–100× 倍速回放，可暂停、调速、循环，可按通道映射到 can0/can1。
- **报文记录**：把总线报文记录为 ASC 文件（CANoe/CANalyzer、python-can 可直接打开），可按大小自动分割。
- **CAN 配置与诊断**：波特率、采样点、CAN-FD 数据段波特率、总线状态、错误计数。
- **CAN 时间同步**：周期发送 0x5A4 (EEA2.1) / 0x594 (EEA3.0) 时间同步报文。
- **WiFi**：扫描、连接（含隐藏网络）、自动获取 IP、已保存网络管理（NetworkManager 或 wpa_supplicant）。
- **串口终端、文件管理、系统监控、Chrony 时间同步状态、dmesg**。

界面为 VS Code 风格深色主题，适配桌面和手机浏览器。

---

## 一、快速安装（推荐）

1. 在 GitHub Releases（或 Actions 构建产物）下载对应架构的安装包：
   - `rkcan-<版本>-linux-arm64.tar.gz`：64 位系统（RK3566/3568/3588/3576 等）
   - `rkcan-<版本>-linux-arm32.tar.gz`：32 位系统（RK3288 等）
2. 拷贝到板卡并安装（需要 root）：

```sh
scp rkcan-*-linux-arm64.tar.gz root@<板卡IP>:/tmp/
ssh root@<板卡IP>
cd /tmp && tar xzf rkcan-*-linux-arm64.tar.gz && cd rkcan-*-linux-arm64
sh install.sh
```

3. 浏览器打开 `http://<板卡IP>/`。

安装程序会自动：

- 识别 systemd / SysV / BusyBox（Buildroot）启动方式，设置开机自启；
- 进程异常退出后 2 秒内自动重启；
- 开机时按配置文件设置 CAN 接口（波特率、CAN-FD、自动 bus-off 恢复、发送队列长度）；
- 再次安装（升级）时保留原有配置，新的默认配置另存为 `rkcan.conf.new`。

在开发电脑上一键部署：Linux/macOS 用 `./deploy.sh root@<板卡IP> arm64`；Windows 双击 `build.bat`，然后按提示用 `scp` 拷贝 `dist\rkcan-linux-arm64` 目录并执行 `sh install.sh`。

### 日常管理

```sh
rkcan-ctl status      # 运行状态
rkcan-ctl restart     # 重启
rkcan-ctl log         # 查看日志
rkcan-ctl config      # 编辑配置并重启
rkcan-ctl can         # 重新设置 CAN 接口并重启
sh uninstall.sh       # 卸载（加 --purge 同时删除配置）
```

### 运行依赖

- 内核已启用 SocketCAN 驱动（`can0`、`can1`）；
- `iproute2` 的 `ip` 命令（BusyBox 自带的 `ip` 不能设置 CAN 波特率）；
- WiFi 功能需要 NetworkManager（`nmcli`）或 wpa_supplicant（`wpa_cli`），以及 DHCP 客户端（`udhcpc`、`dhcpcd` 或 `dhclient` 之一）；
- Chrony 页面需要 `chronyc`（默认查找 `/userdata/chronyc`，其次 `$PATH`）。

---

## 二、配置

配置文件：`/etc/rkcan/rkcan.conf`，修改后执行 `rkcan-ctl restart`。

| 配置项 | 默认值 | 说明 |
|---|---|---|
| `RKCAN_ADDR` | `10.0.0.22:6000` | UDP 转发目标 |
| `RKCAN_CAN0` / `RKCAN_CAN1` | `can0` / `can1` | CAN 接口，留空表示不使用。记录/回放文件中通道 1 = CAN0，通道 2 = CAN1 |
| `RKCAN_PORT` | `80` | Web 端口，`0` 关闭 Web |
| `RKCAN_AUTH` | 空 | Web 登录，格式 `用户名:密码` |
| `RKCAN_FILEROOT` | `/userdata` | 文件管理根目录，同时作为回放/记录的内部存储 |
| `RKCAN_WIFI` | `wlan0` | WiFi 接口 |
| `RKCAN_BRS` | `false` | 时间同步/演示发送使用 BRS |
| `RKCAN_UDPFLAGS` | `false` | 在 UDP 的 DLC 字节高 4 位携带 FD/BRS/ESI 标志 |
| `RKCAN_TIMESYNC` | 空 | 开机自动在该接口发送时间同步报文 |
| `RKCAN_TIMESYNC_PROTO` | `5A4` | `5A4` 或 `594` |
| `RKCAN_LOGFILE` / `RKCAN_LOGMAX` | 空 / `10` | 日志文件与轮转大小 (MB) |
| `CAN_SETUP` | `yes` | 是否由 rkcan 设置 CAN 接口 |
| `CAN_BITRATE` / `CAN_SAMPLE_POINT` | `500000` / `0.875` | 仲裁段波特率/采样点 |
| `CAN_FD` | `on` | CAN-FD 开关 |
| `CAN_DBITRATE` / `CAN_DSAMPLE_POINT` | `2000000` / `0.8` | 数据段波特率/采样点（BRS 帧使用） |
| `CAN_RESTART_MS` | `100` | bus-off 自动恢复时间 |
| `CAN_TXQUEUELEN` | `1000` | 发送队列长度（回放需要） |
| `CAN1_BITRATE` 等 | | 单独覆盖某一路接口的设置，前缀 `CAN0_` / `CAN1_` |

每个配置项都对应一个命令行参数（`RKCAN_TIMESYNC_PROTO` ↔ `-timesync-proto`），命令行优先；`rkcan -help` 查看全部参数。

---

## 三、使用说明

### 报文回放（TF 卡）

1. 插入 TF 卡，进入「回放 / 记录」页。若卡未自动挂载，点击「挂载」（挂载到 `/mnt/sdcard`）。
2. 在「日志文件」中选择 `.asc` 或 `.blf` 文件（自动搜索存储上 4 层目录内的文件，也可以先通过文件管理上传到 `/userdata`）。
3. 设置每个文件通道发往哪个接口（或不发送）、回放速度、BRS、方向（全部 / 仅 Rx / 仅 Tx）、是否循环。
4. 点击「开始」。回放过程中可暂停/继续、随时改变速度；进度条和统计实时刷新。
5. 拔卡前点击「安全移除」。

说明：

- 速度 `1×` 按原始时间间隔发送；`最快` 不等待，受总线带宽限制。
- 文件中的 CAN-FD 帧发往未开启 FD 的接口时，≤8 字节的帧自动降级为经典帧，更长的帧计入错误。
- 支持的格式：ASC（hex/dec，绝对/相对时间戳，经典帧、远程帧、CANFD 行）；BLF（压缩/未压缩，CAN_MESSAGE、CAN_MESSAGE2、CAN_FD_MESSAGE、CAN_FD_MESSAGE_64）。

### 报文记录

选择保存位置（TF 卡优先）、接口和分割大小，点击「开始记录」。文件名为 `rkcan_YYYYMMDD_HHMMSS.asc`。

### 报文发送与监控（CAN 页）

- 「发送报文」：输入 ID（十六进制）、数据（如 `01 02 03`），勾选扩展帧/CAN-FD/BRS/远程帧，单次发送或按周期发送（次数 0 = 持续）。
- 「实时报文监控」：按接口和 ID 汇总，显示最新数据（变化时高亮）、平均周期、计数，可按接口/ID 过滤、暂停、清空。

### BRS

BRS（Bit Rate Switch）只在接口开启 CAN-FD（`fd on`）并设置了数据段波特率（`dbitrate`）时生效：带 BRS 的帧数据段以 `dbitrate` 发送。接收端统计会显示收到的 BRS 帧数。

---

## 四、UDP 转发协议

每个 UDP 包包含若干帧，每帧格式（多字节整数除特别说明外为小端）：

| 偏移 | 长度 | 内容 |
|---|---|---|
| 0 | 2 | 帧头 `0xAA 0x55` |
| 2 | 2 | 负载长度 N（= 30 + 数据长度） |
| 4 | 4 | 全局序号 |
| 8 | 1 | 通道（0 = CAN0，1 = CAN1） |
| 9 | 8 | UTC 时间戳 (µs) |
| 17 | 4 | 启动后相对时间 (µs) |
| 21 | 8 | UTC 时间戳 (µs) |
| 29 | 4 | CAN ID（**大端**，bit31 = 扩展帧，bit30 = 远程帧） |
| 33 | 1 | DLC（0–15）；开启 `RKCAN_UDPFLAGS` 时 bit4 = FD，bit5 = BRS，bit6 = ESI |
| 34 | 0–64 | 数据 |
| 4+N | 4 | CRC32 (IEEE)，覆盖偏移 4 到 4+N |

`UdpCanFdReceiver/` 为 Windows/Linux 上位机接收示例（`make receiver` 或 CMake 编译，运行 `udp_canfd_recv 6000`）。

---

## 五、从源码编译

需要 Go 1.23+。

```sh
make test       # 格式检查 + go vet + 单元测试（含 -race）
make            # 编译 build/rkcan-linux-{arm64,arm32,amd64}
make package    # 生成安装包 dist/rkcan-<版本>-linux-<arch>.tar.gz
make receiver   # 编译上位机 UDP 接收程序
```

推送到 GitHub 后 CI 自动测试并生成安装包；推送 `v*` 标签时自动发布 Release。

---

## 六、HTTP API（节选）

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/version` | 版本、CAN 接口列表 |
| GET | `/api/sse` | 系统与 CAN 统计（Server-Sent Events，每秒） |
| POST | `/api/can/configure` | 设置波特率 `{interface, bitrate, samplePoint, dbitrate, dsamplePoint, fd, restartMs}` |
| POST | `/api/can/send` | 发送 `{iface, id, ext, fd, brs, rtr, data}` |
| GET/POST | `/api/can/periodic` | 周期任务列表 / 新建（额外 `intervalMs, count`） |
| POST | `/api/can/periodic/stop` | 停止任务 `{id}`（0 = 全部） |
| GET | `/api/can/monitor` | 实时监控数据 |
| GET/POST | `/api/can/timesync` | 时间同步 `{enabled, iface, protocol, brs}` |
| GET | `/api/storage` | 存储卷与未挂载的卡 |
| POST | `/api/storage/mount`, `/api/storage/unmount` | 挂载 / 安全移除 |
| GET | `/api/replay/files`, `/api/replay/info?path=` | 日志文件列表 / 文件信息 |
| POST | `/api/replay/start` | `{path, speed, loop, brs, direction, channelMap:{"1":"can0"}}` |
| POST | `/api/replay/pause`, `/resume`, `/stop`, `/speed` | 回放控制 |
| POST | `/api/record/start`, `/api/record/stop` | 记录 `{dir, ifaces, maxSizeMB}` |
| GET/POST | `/api/wifi/status`, `/scan`, `/connect`, `/disconnect`, `/saved`, `/saved/connect`, `/saved/forget` | WiFi |
| | `/api/serial/*`, `/api/files/*`, `/api/chrony*`, `/api/system/*` | 串口、文件、Chrony、系统 |

设置 `RKCAN_AUTH` 后所有接口都需要 HTTP Basic 认证。

---

## 七、常见问题

- **页面打不开**：`rkcan-ctl status` 看是否运行；`rkcan-ctl log` 查看日志；检查端口是否被占用（修改 `RKCAN_PORT`）。
- **CAN 收不到数据**：在 CAN 页点「Refresh」运行诊断；确认双方波特率、采样点一致，总线两端有 120 Ω 终端电阻。
- **CAN 设置失败 / Operation not supported**：确认使用 iproute2 的 `ip`；控制器不支持 CAN-FD 时把 `CAN_FD` 设为 `off`。
- **回放有「错误」计数**：接口未开启 CAN-FD 却回放长 FD 帧，或总线无应答（无其它节点 ACK）。
- **WiFi 连接后无 IP**：安装 `udhcpc`/`dhcpcd`/`dhclient` 之一。
- **TF 卡无法挂载**：确认内核支持卡的文件系统（FAT32/exFAT/ext4）。
