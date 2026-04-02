# DNS Automatic Traffic Splitting

一个基于 Go 的多协议 DNS 分流代理，支持 `UDP`、`TCP`、`DoH`、`DoT`、`DoQ`，可根据规则、GeoSite、GeoIP 自动选择国内或海外上游，并提供并行返回、共享监听、Web 管理面板和 GitHub Actions 自动构建能力。

## 功能特性

- 支持 `UDP` / `TCP` / `DoH` / `DoT` / `DoQ`
- 支持 `hosts.txt`、`rule.txt`、`GeoSite`、`GeoIP` 多级分流
- 未命中静态规则时自动执行国内 / 海外双路查询并回判
- 同组上游支持竞速返回，避免慢上游拖累首包时延
- 支持并行返回模式，可把多个上游结果聚合后返回给下游
- 支持 `DoH` 同端口不同 `path` 复用
- 支持 `DoT` / `DoQ` 同端口不同 `SNI` 复用
- 支持 Let's Encrypt 自动证书和本地证书
- 提供 Web UI，可直接管理配置、查看日志、测试上游、观察统计
- GitHub Actions 已内置，可用于自动编译二进制发布

## 适用场景

- 国内外 DNS 智能分流
- 给 AdGuard Home、SmartDNS 等下游提供上游解析服务
- 同时接入传统 DNS 和加密 DNS
- 需要多上游容灾、竞速、并行聚合的场景

## 分流流程

请求进入后按以下顺序处理：

1. `hosts.txt`
2. `rule.txt` 精确规则
3. `rule.txt` 正则规则
4. `GeoSite`
5. 国内 / 海外双路查询 + `GeoIP` 回判

普通模式下，同组选最快成功上游结果。  
并行返回模式下，仍然先做同样的分流决策，但会对命中的目标组做多上游聚合。

## 并行返回模式

并行返回用于把多个上游的成功结果聚合给下游，适合下游继续做测速、优选或者容灾。

当前实现包含以下机制：

- 首次请求先返回 `race-first` 结果，降低首包时延
- 后台启动聚合，并将结果写入聚合缓存
- 后续命中 `aggregate-cache` 时直接返回缓存结果
- 支持滑动续期
- 支持热门域名自动续热
- 热门续热带最小触发间隔，避免过热域名反复刷新
- 可配置重复记录 TTL 聚合策略
- 可配置每种记录类型只返回一条，用于跳过下游多 IP 测速策略

## 目录结构

```text
.
|- cmd/
|  `- doh-autoproxy/
|     `- main.go
|- internal/
|  |- client/
|  |- config/
|  |- manager/
|  |- querylog/
|  |- resolver/
|  |- router/
|  |- server/
|  `- web/
|- .github/
|  `- workflows/
|     `- release.yml
|- config.yaml.example
|- Makefile
|- install.sh
|- go.mod
`- README.md
```

## 快速启动

### 手动运行

```bash
go build -o doh-autoproxy cmd/doh-autoproxy/main.go
./doh-autoproxy
```

Windows 下：

```powershell
go build -o doh-autoproxy.exe cmd/doh-autoproxy/main.go
.\doh-autoproxy.exe
```

首次运行时会按配置自动检查并下载 `GeoIP.dat`、`GeoSite.dat`。

### 使用 Makefile

```bash
make windows
make linux-amd64
make linux-arm64
```

输出目录为 `build/`。

### 使用 install.sh 部署（Linux）

`install.sh` 适用于 Linux + systemd 环境，会自动完成：

- 检测架构并下载最新 Release 二进制
- 安装到 `/usr/local/bin/doh-autoproxy`
- 创建配置目录 `/etc/doh-autoproxy`
- 生成 `config.yaml.example`、`hosts.txt`、`rule.txt`
- 写入 systemd 服务文件
- 自动 `daemon-reload` 和 `enable`

在线一键安装：

```bash
curl -fsSL https://raw.githubusercontent.com/Hamster-Prime/DNS_automatic_traffic_splitting/main/install.sh | sudo bash
```

指定命令执行：

```bash
curl -fsSL https://raw.githubusercontent.com/Hamster-Prime/DNS_automatic_traffic_splitting/main/install.sh | sudo bash -s -- install
curl -fsSL https://raw.githubusercontent.com/Hamster-Prime/DNS_automatic_traffic_splitting/main/install.sh | sudo bash -s -- update
```

如果你在 fork 或测试分支上使用，也可以覆盖仓库或版本：

```bash
curl -fsSL https://raw.githubusercontent.com/<your-user>/<your-repo>/<branch>/install.sh | \
  sudo DOH_AUTOPROXY_REPO="<your-user>/<your-repo>" DOH_AUTOPROXY_RELEASE_TAG="latest" bash
```

常用命令：

```bash
sudo bash install.sh install
sudo bash install.sh update
sudo bash install.sh status
sudo bash install.sh restart
sudo bash install.sh logs
```

依赖说明：

- 需要 `bash`
- 需要 `curl` 或 `wget`
- 建议运行环境为 Linux + systemd
- Debian / Ubuntu 通常需要：`curl`、`ca-certificates`、`systemd`
- CentOS / RHEL / Rocky / AlmaLinux 通常需要：`curl`、`ca-certificates`、`systemd`

安装完成后，主配置文件路径为：

```text
/etc/doh-autoproxy/config.yaml
```

服务名为：

```text
doh-autoproxy
```

如果服务尚未启动，可在完成配置后执行：

```bash
sudo systemctl start doh-autoproxy
```

## 配置示例

```yaml
listen:
  dns_udp: "53"
  dns_tcp: "53"
  doh: "443"
  doh_path: "/dns-query"
  dot: "853"
  dot_sni: "dns.example.com"
  doq: "853"
  doq_sni: "dns.example.com"

bootstrap_dns:
  - "udp://223.5.5.5:53"
  - "udp://1.1.1.1:53"

upstreams:
  cn:
    - address: "223.5.5.5"
      protocol: "udp"
      ecs_ip: "114.114.114.114"
  overseas:
    - address: "1.1.1.1"
      protocol: "doh"
      ecs_ip: "8.8.8.8"
      http3: true

parallel_return:
  enabled: true
  warm_cache_ttl: 5
  aggregate_cache_ttl: 30
  aggregate_cache_ttl_mode: "fixed"
  aggregate_ttl_strategy: "median"
  single_record_per_type: false
  listen:
    dns_udp: "5353"
    dns_tcp: "5353"
    doh: "443"
    doh_path: "/multi/dns-query"
    dot: "853"
    dot_sni: "parallel.example.com"
    doq: "853"
    doq_sni: "parallel.example.com"
  upstreams:
    cn:
      - address: "223.5.5.5"
        protocol: "udp"
        ecs_ip: "114.114.114.114"
      - address: "223.6.6.6"
        protocol: "dot"
        ecs_ip: "114.114.114.114"
    overseas:
      - address: "1.1.1.1"
        protocol: "doh"
        ecs_ip: "8.8.8.8"
        http3: true
      - address: "8.8.8.8"
        protocol: "dot"
        ecs_ip: "8.8.8.8"
```

## 并行返回关键配置

- `warm_cache_ttl`
  - 首包竞速结果返回给下游时的 TTL
- `aggregate_cache_ttl`
  - 聚合缓存固定时长或回退时长
- `aggregate_cache_ttl_mode`
  - `fixed`：固定秒数
  - `upstream`：跟随聚合结果中的上游 TTL，若无法提取则回退到 `aggregate_cache_ttl`
- `aggregate_ttl_strategy`
  - 多个上游返回相同记录但 TTL 不同时的聚合策略
  - 可选：`min` / `median` / `max` / `avg`
- `single_record_per_type`
  - 仅在 `race-first` 首包竞速响应时生效
  - 开启后每种记录类型只返回一条，例如 `A` 只返回一条，`AAAA` 只返回一条
  - `aggregate-cache` 命中后仍返回完整聚合结果

## 共享监听规则

### DoH 端口复用

主服务和并行返回可复用同一个 DoH 端口，但 `doh_path` 必须不同。

```yaml
listen:
  doh: "443"
  doh_path: "/dns-query"

parallel_return:
  enabled: true
  listen:
    doh: "443"
    doh_path: "/multi/dns-query"
```

### DoT / DoQ 端口复用

主服务和并行返回可复用同一个 DoT / DoQ 端口，但 SNI 必须不同。

```yaml
listen:
  dot: "853"
  dot_sni: "dns.example.com"

parallel_return:
  enabled: true
  listen:
    dot: "853"
    dot_sni: "parallel.example.com"
```

## Web 管理面板

Web UI 支持：

- 配置读写与热重载
- 上游连通性测试
- 查询日志
- 请求协议、监听端口、普通 / 并行模式日志标记
- `race-first` / `aggregate-cache` 统计
- 热门续热触发次数
- 上游性能统计（普通组和并行组）

## 常见用途

- 作为本地或服务器上的 DNS 分流代理
- 作为 AdGuard Home 上游
- 作为支持加密 DNS 的统一入口
- 作为并行聚合型多上游解析器

## License

MIT
