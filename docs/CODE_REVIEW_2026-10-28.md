# 代码审查与修复记录（2026-10-28）

审查基线：`b8cd3fc`。开始时工作区无未提交改动。范围包括指标采集、渲染、输出传输、Web 配置与连接生命周期，以及 Linux 发布包的启动入口。

现有的采集器分工、渲染快照、异步输出和有界帧队列可以继续沿用。本轮没有更换架构或传输协议，重点修复能够从源码、可控输入和回归测试确认的问题。

## 已修复的问题

| 优先级 | 问题及影响 | 修复与验证 |
| --- | --- | --- |
| P1 | AX206 USB 读写没有超时；设备停止响应 ACK 时，输出线程和等待它的关闭流程可能永久阻塞。 | 使用可取消的端点读写，关闭时先取消；单条 SCSI 命令总超时为 5 秒，与现有网络输出默认超时一致。阻塞端点测试验证取消及截止时间；Linux/Windows 实现同步。 |
| P1 | HTTP/TCP 关闭先等待输出线程；正在等待远端的请求可能阻塞退出或配置切换，最长达到配置允许的 10 分钟。 | HTTP 取消请求并清理闲置连接；TCP 取消拨号、关闭活动连接后等待退出。使用阻塞 HTTP transport 和 TCP pipe 验证。 |
| P1 | 保存请求发出后继续编辑，响应到达时把最新编辑内容当作已保存内容，导致脏状态和撤销历史丢失。 | 已提交基线改为实际发送的配置快照；保留保存期间新增编辑的脏状态与撤销历史。覆盖正常保存、并发编辑及保存失败。 |
| P2 | 停止 Web 服务没有关闭升级后的 WebSocket，旧连接及推送协程继续存活，旧客户端仍能操作旧 store。 | 关闭 store 的全部连接，等待读取、写入、推送结束，再释放共享运行时；关闭后拒绝注册新连接。覆盖关闭、重复关闭及新 store 接入。 |
| P2 | 磁盘后台缓存刷新读取动态计数器/温度，实际采集器又读取一次；仅启用静态指标时也会读取计数器。 | 后台缓存只刷新静态信息；发现时在副本上补动态信息；运行时按启用指标批量采样。双盘测试验证每轮各一批、100 MiB/s 差分、温度，以及关闭动态指标时跳过对应采样。 |
| P2 | Linux 的 Guest/GuestNice 已包含在 User/Nice 中，又被加入总 CPU 时间；虚拟机宿主的利用率可能偏高。 | 仅在 Linux 排除重复的 guest 时间。回归输入 User=50、Idle=50、Guest=50 的差分结果从错误的 66.67% 恢复为 50%。 |
| P2 | `hwmon` 容器目录被当作 `hwmonN` 端点，标准 SATA/drivetemp 温度路径不可达。 | 正确遍历 `device/hwmon/hwmonN`，保留直接端点路径支持；临时目录测试读取 42°C。 |
| P2 | Zram/Btrfs 重新发现时重建全部指标，丢失对象身份、值和启用/可用状态。 | 指标只初始化一次；通过 manager 的实际发现路径验证状态保留。 |
| P2 | CSS 非预乘 RGBA 值被存入 Go 的预乘 `color.RGBA`，半透明颜色偏亮。 | 改用 `color.NRGBA`，同步调整 alpha 转换；验证 rgba/hex 和实际 gg 像素合成。 |
| P2 | 简洁曲线历史值超出量程时，Y 坐标越界，可能画到邻近组件。 | 与完整曲线保持一致，夹取 Y 坐标；像素测试覆盖低于最小值、高于最大值。 |
| P2 | 字体缓存只在启动时建立，切换/预览字体不生效；Web 模式首次还可能读取上一份全局配置。 | 根据显式配置选择字体；实际字体路径改变才重建缓存。临时 TTF 验证初次加载、预览、切换、缓存复用和字形度量变化。 |
| P2 | 独立采集器启停 API 没有同步活动 profile，并忽略写入错误；重启会覆盖刚保存的启停设置。 | 同步保存当前配置和活动 profile，成功后更新 store/runtime，失败返回错误；覆盖重启恢复、两类写失败及未知采集器。 |
| P2 | Linux 压缩包没有 `.desktop` 文件或安装入口，退出后无法从应用菜单找到程序。 | 包内加入 `install.sh`、desktop 文件和复用现有图标；在隔离用户目录验证安装、重复安装、移除解压目录后启动及特殊路径。 |

相关实现和测试位于 `src/metrics_render_sender/`、`frontend/tests/`、`packaging/linux/` 和 `tests/test_linux_install.py`。

## Linux 安装行为

解压后使用普通桌面用户执行 `./install.sh`，不需要 sudo。程序安装到：

```text
$HOME/.local/lib/metrics_render_sender/metrics_render_sender
${XDG_DATA_HOME:-$HOME/.local/share}/applications/metrics_render_sender.desktop
${XDG_DATA_HOME:-$HOME/.local/share}/icons/hicolor/scalable/apps/metrics_render_sender.svg
```

之后可从应用菜单搜索 **MetricsRenderSender** 启动，也可以继续直接运行压缩包内的程序。安装入口使用固定的安装路径，解压目录可以删除。重复安装通过替换文件更新程序；当前已运行的进程需要重启。

路径遵循 [XDG Base Directory Specification](https://specifications.freedesktop.org/basedir/latest/)，Exec 参数按 [Desktop Entry Specification](https://specifications.freedesktop.org/desktop-entry/latest/exec-variables.html) 转义。通过 `gio launch` 验证了空格、中文、百分号、引号、反斜杠、美元符号及反引号路径。

## 性能边界与保留的行为

- 现有代码始终使用全量采集：`WebAPI.isFullMode()` 返回 true，manager 初始化时也开启全量模式。本轮保留这一行为。若改为后台仅采当前布局所需指标，需要明确未展示指标的实时更新要求，并覆盖编辑器连接、断开和布局切换；不能只修改一个布尔返回值。
- 每个输出帧仍生成预览 PNG，即使没有编辑器连接。这也支撑现有预览接口和帧大小指标；尚未修改为按需编码，也没有声称获得相应 CPU 降幅。
- 前端生产 JS 约 1.55 MB，gzip 约 424 KB，Vite 仍提示单个 chunk 较大。分包属于后续首屏加载优化，本轮没有展开。
- 磁盘优化的验证依据是采样批次数和输出值，没有运行跨硬件性能基准，因此不量化整机 CPU 降幅。
- 配置与活动 profile 仍沿用既有的两文件保存流程；本轮修复错误上报和运行状态生效顺序，没有引入跨文件事务。

## 验证

本机为 Arch Linux、Go 1.23.1、Node.js 24.15.0。已安装的是 legacy AppIndicator，因此本机 Go 验证使用 `legacy_appindicator` 标签；默认发布工作流的 Ayatana 依赖没有改动。

```bash
cd src/metrics_render_sender
go test -race -tags legacy_appindicator ./...
go vet -tags legacy_appindicator ./...

cd ../..
node --test frontend/tests/*.test.mjs
python3 -B -m unittest discover -s tests -v
shellcheck packaging/linux/install.sh
bash -n packaging/linux/install.sh package.sh
desktop-file-validate packaging/linux/metrics_render_sender.desktop
git diff --check

cd frontend
npm ci --include=dev
npm run build

cd ..
BUILD_TARGETS='linux windows' GOFLAGS='-tags=legacy_appindicator' \
  SKIP_FRONTEND_BUILD=1 VERSION=1.0.0-review ./package.sh
```

Go 测试和竞态检测、静态检查、3 项前端保存回归、3 项安装启动回归、前端构建以及 Linux/Windows 交叉构建打包均通过。`SKIP_FRONTEND_BUILD=1` 的打包使用同步到 `webassets/webdist` 的本次前端构建产物。

对最终 Linux 压缩包还执行了隔离用户目录冒烟测试：解压并安装后移除解压目录，已安装程序能列出指标；实际 Web 服务能返回内嵌页面、实时指标和 80×40 PNG 预览；通过 HTTP API 禁用 CPU 采集器后，完整重启进程仍保留禁用状态。临时进程及文件均已清理。

未进行真实 AX206 硬件故障注入、Windows 实机运行、KVM 负载或真实 SATA/drivetemp 传感器验证。真实 GitHub 更新集成测试需要显式环境变量，未启用。审查验证阶段未提交或发布；后续发布以 Git 标签和 GitHub Release 为准，`dist/` 中为本机验证产物。
