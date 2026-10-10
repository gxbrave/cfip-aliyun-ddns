# CFIP Aliyun DDNS

仅支持 **Linux x64 / amd64**。主程序为 Go 静态二进制，运行不需要 Go、Python、Node 或其他语言环境。使用随包提供的 `cfst` 完成 Cloudflare IP 优选，使用阿里云 DNS API 创建或更新 A 记录。

## 配置

复制 `.env.example` 为 `.env`，填写：

```dotenv
MIN_SPEED=5
MAX_LATENCY=200
ALIYUN_DOMAIN=cf.example.com
ALIYUN_ACCESS_KEY_ID=你的ID
ALIYUN_ACCESS_KEY_SECRET=你的Secret
DOWNLOAD_NUMBER=10
DOWNLOAD_URL=https://download.parallels.com/desktop/v17/17.1.1-51537/ParallelsDesktop-17.1.1-51537.dmg
```

- `MIN_SPEED`：下载速度下限，MB/s，可为零。
- `MAX_LATENCY`：域名 ICMP ping 与 cfst 优选的平均延迟上限，ms，必须大于零。
- `ALIYUN_DOMAIN`：需要更新的完整域名，支持主域名、子域名及 `@` 主域记录（填写完整主域名）。
- 阿里云密钥需要读取域名记录及创建、修改记录的权限。停用的 A 记录还需要启用记录的权限。

程序默认读取**自身可执行文件目录**中的 `.env`，可用 `--env-file` 指定其他文件。进程环境变量覆盖 `.env`；`.env` 支持 `KEY=value`、单/双引号和注释行，不进行 shell 展开，也不执行内容。Secret 不写入日志。

五项必填命令行参数必须**全部出现**才生效。只传其中部分时，整组五项参数全部忽略，改读环境变量 / `.env`；全部传入但值不合法则报错。可选参数独立覆盖环境设置。

```bash
./cfip-ddns --min-speed 5 --max-latency 200 \
  --aliyun-domain cf.example.com \
  --aliyun-access-key-id YOUR_ID --aliyun-access-key-secret YOUR_SECRET
```

建议使用受权限保护的 `.env`，避免将密钥写到命令历史或进程参数中。

| 可选参数 | 环境变量 | 默认值 |
| --- | --- | --- |
| `--download-number` | `DOWNLOAD_NUMBER` | `10` |
| `--download-url` | `DOWNLOAD_URL` | Parallels DMG URL，见 `.env.example` |
| `--cfst` | `CFST_PATH` | 程序目录下 `cfst` |
| `--ip-file` | `IP_FILE` | 程序目录下 `ip.txt` |
| `--data-dir` | `DATA_DIR` | 程序所在目录 |
| `--check-interval` | `CHECK_INTERVAL` | `15m` |
| `--cfst-timeout` | `CFST_TIMEOUT` | 每阶段 `6h` |
| `--dns-ttl` | `DNS_TTL` | `600` 秒 |

测速数量和地址可以直接在 `.env` 或 `/opt/cfip-ddns/.env` 中修改，服务重启后生效；也可以独立使用参数覆盖，不必同时传五个必填参数：

```bash
./cfip-ddns --download-number 20 --download-url https://example.com/test.bin
```

这两个选项不纳入五项必填参数的整组回退规则，仍保持命令行 > 进程环境变量 > `.env` > 默认值。

相对路径按工作目录解释。阿里云账号套餐可能限制最小 TTL，应设置为套餐允许的值。

## 运行行为

启动即检测域名，之后每 15 分钟发送 4 次 ICMP echo（ping）检测，间隔 1 秒，不依赖系统 ping 命令；单次等待至少 1 秒、至多 10 秒。该变更只影响域名健康检测，cfst 优选本身仍使用其默认 TCP 测速。通过 `https://dns.alidns.com/dns-query` 的标准 DNS wire format 解析 A 记录；DoH 主机名使用 `119.29.29.29:53` bootstrap DNS 解析，HTTPS 保持正常证书校验。域名解析失败立即优选；任一 A 记录延迟超限或 4 次 ICMP ping 少于 3 次成功也触发优选。

每天固定 **UTC+8 03:00 和 15:00** 直接使用 `ip.txt` 完整优选，跳过 `bestip.txt` 缓存阶段；无结果仍执行保底优选，成功后仍合并更新缓存。与机器时区无关。优选串行执行，执行期间不启动第二个测速；错过的检查不积压，长测速期间的定时任务合并执行。服务停机期间不会补跑历史任务。

域名异常检测和手动 `--once` 的优选流程：

1. 若数据目录存在非空 `bestip.txt`，先用它进行优选；有满足条件的结果就返回首条。
2. 使用 `ip.txt` 完整优选；返回首条，将所有满足条件的结果合并到 `bestip.txt` 并去重。
3. 无结果则再次使用 `ip.txt`，保留速度下限，放宽延迟条件。cfst 自身仍有默认 9999ms 上限。

三个阶段共同参数：`-t 4 -dt 6 -tlr 0.3 -url URL -sl MIN_SPEED -dn DOWNLOAD_NUMBER -f FILE -o RESULT`；前两阶段额外传 `-tl MAX_LATENCY`。**不使用 `-allip`**，使用 cfst 默认随机采样模式。阶段执行完毕以子进程退出为准，CSV 生成并不代表测速结束。程序再次校验 CSV 的速度、延迟、丢包率，忽略未达到条件的行，清理 `result.csv`，不会读取残留 CSV。

优选结果写到数据目录 `selected-ip.txt`，通过 DoH 对比当前 A 记录；不一致或解析失败才调用阿里云 API。没有 A 记录则创建；已有同名 A 记录则更新，保留线路；多个线路的 A 记录统一为优选 IP。已有 CNAME 会报错，需先自行处理冲突。AAAA / MX / TXT 等记录保留。更新失败保留优选结果，下次检查重试；DoH 有缓存，短时间内仍可能看到旧值。

没有可用结果则保留原 DNS，下次检查重试。状态文件使用原子写入；同一数据目录使用文件锁，避免多个实例同时测速。SIGTERM / SIGINT 会取消网络请求并终止 cfst。日志时间使用 UTC。

`bestip.txt` 统一存放在数据目录。若已有旧缓存，可在服务停止时复制到该目录，并确保服务用户有读写权限。

## 一键安装与公开仓库

仓库地址：[https://github.com/gxbrave/cfip-aliyun-ddns](https://github.com/gxbrave/cfip-aliyun-ddns)。项目只支持 Linux amd64。下面的命令下载已构建的 amd64 发行包，不需要 Go：

```bash
# GitHub 官方地址
curl -fsSL https://raw.githubusercontent.com/gxbrave/cfip-aliyun-ddns/main/scripts/install-online.sh | sudo sh

# 国内加速：脚本本身和发行包都通过 ghfast.top
curl -fsSL https://ghfast.top/https://raw.githubusercontent.com/gxbrave/cfip-aliyun-ddns/main/scripts/install-online.sh | sudo CFIP_MIRROR=ghfast sh
```

安装脚本会校验 SHA-256，并把程序、配置、cfst、IP 列表和状态放入 `/opt/cfip-ddns`。首次安装后编辑 `/opt/cfip-ddns/.env`，再执行 `sudo systemctl enable --now cfip-ddns`。升级时重复运行同一命令，会保留现有配置和缓存。可通过 `CFIP_VERSION=v1.0.0`、`CFIP_MIRROR=github|ghfast`、`CFIP_REPO=owner/repo` 覆盖默认值。

## Debian / systemd

在构建机器安装 Go 1.23+，运行：

```bash
./scripts/build.sh
```

生成的 `dist/cfip-ddns-linux-amd64.tar.gz` 可直接复制到 Debian x64，目标机器无需 Go。目标机器需要常规系统 CA 证书（Debian 的 `ca-certificates`），以验证 HTTPS：

```bash
tar -xzf cfip-ddns-linux-amd64.tar.gz
# 填好配置；或在安装后编辑 /opt/cfip-ddns/.env
cp .env.example .env
nano .env
sudo ./scripts/install.sh
sudo systemctl enable --now cfip-ddns
sudo systemctl status cfip-ddns
sudo journalctl -u cfip-ddns -f
```

安装脚本只安装、不自动启动；升级时会停止旧服务，安装后需手动启动。程序位于 `/opt/cfip-ddns`，配置位于 `/opt/cfip-ddns/.env`，缓存、优选结果和锁文件也位于 `/opt/cfip-ddns`。配置默认 root 所有、服务组可读，服务以专用低权限用户运行。应用目录允许服务用户写入，ICMP raw socket 回退只授予 `CAP_NET_RAW`。服务文件也放在应用目录，`/etc/systemd/system` 仅保留 systemd 必需的链接。旧版配置与状态自动迁移，原文件备份位于应用目录的 `migration-backup`。安装不会覆盖已有配置和缓存。

```bash
sudo systemctl stop cfip-ddns
sudo systemctl restart cfip-ddns
sudo systemctl disable --now cfip-ddns
```

修改配置后重启服务。也可以直接运行 `./cfip-ddns`。

## Docker

从 GitHub 获取完整 Docker 配置（不会把 `.env` 提交到仓库）：

```bash
git clone https://github.com/gxbrave/cfip-aliyun-ddns.git
# 国内网络可使用：git clone https://ghfast.top/https://github.com/gxbrave/cfip-aliyun-ddns.git
cd cfip-aliyun-ddns
cp .env.example .env
# 编辑 .env 后构建并启动
docker compose up -d --build
```

```bash
cp .env.example .env
# 编辑 .env 后：
docker compose up -d --build
docker compose logs -f
docker compose down
```

或者使用单一容器：

```bash
docker build --platform linux/amd64 -t cfip-aliyun-ddns:local .
docker run -d --name cfip-ddns --restart unless-stopped \
  --env-file .env --sysctl net.ipv4.ping_group_range="0 2147483647" \
  -v cfip-data:/app/data cfip-aliyun-ddns:local
```

容器使用 UID 10001，镜像自带证书、cfst、IP 文件，只有一个工作目录 `/app`：程序、cfst、IP 文件在 `/app`，持久化状态在 `/app/data`；配置从宿主项目目录的 `.env` 注入环境变量。`.env` 不进入镜像构建上下文。如果改成绑定宿主目录，须事先授予 UID 10001 写入权限。systemd 与 Docker 请选一种运行，避免同一域名被两个实例更新。

## 检查与手动优选

```bash
# 只读：验证 DoH 和阿里云记录读取；域名未创建时 DoH 检查会报错
./cfip-ddns --check
# 测速验证，不写 DNS、不修改 selected-ip.txt（仍会更新 bestip 缓存）
./cfip-ddns --once --dry-run
# 立即执行一次真实优选并同步 DNS
./cfip-ddns --once
```

手动优选与后台实例共享数据目录时会被文件锁阻止，先停止后台服务。`--check` 不占用状态目录锁，可与后台服务同时运行。Docker 对应命令：`docker compose run --rm cfip-ddns --check`。

```bash
go test -race ./...
go vet ./...
```

测试覆盖配置整组回退、三阶段优选与异常、CSV 条件过滤、缓存去重、子进程取消、UTC+8 调度、ICMP 报文校验与本机 ping、DNS 报文和阿里云签名/记录创建更新；API 写入测试使用本地模拟服务。

## 文件与上游

`cfst` 是用户提供的 CloudflareSpeedTest v2.3.5 Linux amd64 可执行文件，上游：https://github.com/XIU2/CloudflareSpeedTest 。测速可用性取决于测试机网络及下载 URL；下载失败时可替换 `DOWNLOAD_URL`，不会自动降低速度要求。分发 cfst 时请遵守上游许可要求。
