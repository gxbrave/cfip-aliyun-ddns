# 验证记录

验证日期：2026-10-10。目标平台 Linux amd64。

| 检查 | 结果 |
| --- | --- |
| `go test -race ./...` | 通过 |
| `go vet ./...` | 通过 |
| Docker 镜像构建、Compose 配置解析 | 通过 |
| 静态二进制 | Linux amd64，CGO 关闭 |
| Debian 服务器运行 | 192.168.6.22，x86_64 |
| systemd 安装脚本 | 安装到 `/opt/cfip-ddns`，已按用户要求启用并启动 |
| 正式 systemd unit | 专用服务用户、严格文件系统限制下 dry-run 通过，退出码 0 |
| Docker 运行 | UID 10001 下使用模拟 cfst 的 dry-run 通过 |
| Docker SIGTERM 停止 | 取消 cfst，退出码 0 |
| 阿里云真实只读 API | 成功读取主域名和记录列表 |
| 阿里云 DNS 创建 / 更新 / 启用 / 冲突 | 模拟 API 测试通过；随后真实创建 A 记录成功，DoH 已确认生效 |
| 实际缓存复测 | 按 80ms / 20MB/s 重新检测；本次缓存无合格结果，正确转入完整优选，随后主动停止测试 |
| 实际完整优选 | 20 MB/s、80 ms 阈值下成功，未使用 `-allip` |

实际完整优选从 UTC 05:50:45 到 05:54:55，约 4 分 10 秒。cfst 本次随机采样 11345 个地址，找到 10 个符合条件的候选。首选 `104.19.42.225`，CSV 延迟 25.36ms，下载速度 89.98MB/s。`result.csv` 已由程序清理，候选已保存到 `bestip.txt`。

运行使用 `--once --dry-run`，没有更改真实 DNS。测试域名在验证时没有 A 记录，DoH 返回 NXDOMAIN；因此 `--check` 在完成阿里云记录读取后返回解析错误属于预期行为。测速结果随网络和时间变化，不保证未来维持相同速度。

测试服务器上配置位于 `/etc/cfip-ddns.env`，权限 `640 root:cfip-ddns`；候选缓存已复制到 `/var/lib/cfip-ddns/bestip.txt`。正式服务已于 UTC 05:58:57 按用户要求启用并启动，状态为 `active/running`、`enabled`。本次缓存优选选出 `104.17.164.189`（63.36ms、37.37MB/s），于 UTC 05:59:10 成功创建 `yd.wcop.cn` 的 A 记录。UTC 05:59:45 通过 DoH 确认解析为该 IP，阿里云 API 确认记录已存在。

## 配置与定时优选更新

已显式补充本地 `.env` 与服务器 `/etc/cfip-ddns.env` 的 `DOWNLOAD_NUMBER=10`、`DOWNLOAD_URL`。新增测试验证这两项参数可以独立覆盖环境配置，以及定时优选跳过有效缓存、完整结果仍合并缓存、无结果进入保底阶段。`go test -race ./...`、`go vet ./...` 和 Docker 构建通过。

新版二进制已部署并重启，服务器与本地发行二进制 SHA-256 一致，服务仍为 `active/running`、`enabled`。每日 UTC+8 03:00、15:00 调用完整优选；域名异常检测与手动 `--once` 继续先检查缓存。


## ICMP ping 与统一目录更新

`go test -race ./...`、`go vet ./...`、Docker 镜像构建和 Compose 配置解析通过。新增 ICMP 报文校验、本机 4 次 ping 和取消测试；Docker UID 10001 下同样通过，使用命名空间 `ping_group_range` 支持无特权 ping socket。

服务器已迁移并重启，当前配置为 `/opt/cfip-ddns/.env`，二进制、cfst、ip.txt、bestip.txt、selected-ip.txt、service.lock、服务文件均在同一应用目录。原 `/etc/cfip-ddns.env` 与 `/var/lib/cfip-ddns` 已迁移清理；历史测试文件与旧配置状态备份位于 `/opt/cfip-ddns/migration-backup`。系统服务目录只保留指向应用目录服务文件的链接。

服务器真实 ping：`104.18.33.131`，4/4 成功，平均 15.09ms；保留用户服务器现有配置（测速下限 40MB/s、延迟上限 80ms）。Docker 真实 ping：同一 IP，4/4 成功，平均 16.04ms，停止退出码 0。Docker 统一工作目录 `/app`，状态 volume 位于 `/app/data`，配置从宿主项目 `.env` 注入。服务器服务仍为 active/running、enabled，部署二进制 SHA-256 与本地发行一致。
