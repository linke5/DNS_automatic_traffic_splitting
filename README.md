# DNS Automatic Traffic Splitting

高性能 · 多协议 · 智能分流 · 可视化管理  
一个使用 Go 编写的现代化 DNS 代理服务，自动根据 GeoIP/GeoSite 智能分流国内外流量。

## 为什么选择这个项目？

在中国大陆网络环境中，DNS 解析通常面临两个问题：

- 国内域名需要国内 DNS 才能获得更优 CDN 节点
- 海外域名需要海外 DNS 才能避免污染、劫持或错误解析

这个项目通过 `GeoSite + GeoIP + 双路并发兜底` 自动完成国内外分流，同时提供 Web 管理面板，减少手工维护规则的复杂度。

## 核心亮点

| 特性 | 说明 |
|:---|:---|
| 全协议覆盖 | 支持 `UDP` / `TCP` / `DoT` / `DoQ` / `DoH`（HTTP/1.1、HTTP/2、HTTP/3） |
| 智能分流 | `Hosts -> Rule -> GeoSite -> 双路并发 + GeoIP 回判` |
| 并发竞速 | 同组上游并发查询，优先返回成功结果，失败响应不会抢占正确结果 |
| 并行返回 | 在保留 Geo 分流策略的前提下，聚合同组多个上游的成功解析结果返回给下游 |
| 共享监听 | `DoH` 支持同端口不同 `path`，`DoT/DoQ` 支持同端口不同 `SNI` |
| Bootstrap 解析 | 内置独立引导解析器，支持缓存和多服务器重试，避免循环依赖 |
| ECS 优化 | 支持 EDNS Client Subnet，帮助 CDN 返回更优节点 |
| 自动证书 | 支持 Let's Encrypt 自动证书和本地证书 |
| Web 面板 | 支持配置管理、日志查询、上游测试、运行状态查看 |
| 日志增强 | 可记录请求协议、监听端口、普通/并行服务模式 |

## 快速开始

### 一键安装（Linux）

```bash
bash <(curl -sL https://raw.githubusercontent.com/Hamster-Prime/DNS_automatic_traffic_splitting/main/install.sh)
```

脚本会自动完成：

- 下载最新二进制
- 创建配置目录
- 生成 `config.yaml.example`
- 注册 systemd 服务
- 设置开机自启

### 手动运行

1. 从 Releases 下载对应平台二进制
2. 准备 `config.yaml`
3. 运行程序

```bash
chmod +x doh-autoproxy-linux-amd64
./doh-autoproxy-linux-amd64
```

首次运行会自动下载 `GeoIP.dat` 和 `GeoSite.dat`。

### Docker

```bash
docker run -d \
  --name dns-proxy \
  --restart always \
  --network host \
  -v $(pwd)/config:/app/config \
  -v $(pwd)/certs:/app/certs \
  weijiaqaq/dns_automatic_traffic_splitting:latest
```

建议使用 `host` 网络模式，以获得更好的 UDP 和 QUIC 性能。

## 配置示例

### 基础监听

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
```

### 上游分组

```yaml
upstreams:
  cn:
    - address: "223.5.5.5"
      protocol: "udp"
      ecs_ip: "114.114.114.114"
    - address: "223.6.6.6"
      protocol: "dot"
      ecs_ip: "114.114.114.114"
      pipeline: true

  overseas:
    - address: "1.1.1.1"
      protocol: "doh"
      ecs_ip: "8.8.8.8"
      http3: true
    - address: "8.8.8.8"
      protocol: "dot"
      ecs_ip: "8.8.8.8"
      pipeline: true
```

### 并行返回模式

这个模式适合把多个解析结果交给下游 DNS 客户端继续测速或择优，例如 `AdGuard Home`。

```yaml
parallel_return:
  enabled: true
  listen:
    dns_udp: "5353"
    dns_tcp: "5353"
    doh: "443"
    doh_path: "/parallel-dns-query"
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

## 共享监听规则

### DoH 端口复用

主服务和并行返回可以复用同一个 `DoH` 端口，但 `doh_path` 必须不同。

正确示例：

```yaml
listen:
  doh: "443"
  doh_path: "/dns-query"

parallel_return:
  enabled: true
  listen:
    doh: "443"
    doh_path: "/parallel-dns-query"
```

错误示例：

```yaml
listen:
  doh: "443"
  doh_path: "/dns-query"

parallel_return:
  enabled: true
  listen:
    doh: "443"
    doh_path: "/dns-query"
```

### DoT / DoQ 端口复用

主服务和并行返回可以复用同一个 `DoT` 或 `DoQ` 端口，但 `SNI` 必须不同。

正确示例：

```yaml
listen:
  dot: "853"
  dot_sni: "dns.example.com"
  doq: "853"
  doq_sni: "dns.example.com"

parallel_return:
  enabled: true
  listen:
    dot: "853"
    dot_sni: "parallel.example.com"
    doq: "853"
    doq_sni: "parallel.example.com"
```

如果主服务和并行返回复用相同端口，但 `path` 或 `SNI` 冲突，前端不会允许保存，后端也会拒绝加载配置。

## 分流策略

请求进入后，会按以下顺序处理：

1. `hosts.txt`
2. `rule.txt` 精确匹配
3. `rule.txt` 正则匹配
4. `GeoSite`
5. 国内 / 海外双路并发查询，再用 `GeoIP` 判断结果

未命中静态规则时，系统会同时向国内和海外上游发起查询，并根据返回 IP 的地理位置选择更合适的结果。

## 自定义文件

### `hosts.txt`

```text
192.168.1.1 myrouter.lan
192.168.1.100 nas.home
0.0.0.0 ads.example.com
```

### `rule.txt`

```text
google.com overseas
baidu.com cn
regexp:.*\.google\..* overseas
regexp:.*\.aliyun\..* cn
```

## Web 管理面板

默认地址示例：`http://your-server:8080`

支持的功能：

- 仪表盘统计
- 查询日志分页与搜索
- 上游服务器测试
- 可视化编辑 `listen`、`upstreams`、`parallel_return`
- 管理 `hosts` 条目
- 查看普通组和并行组请求

当前日志可记录：

- 客户端 IP
- 下游 ECS
- 查询域名和类型
- 分流策略
- 返回结果
- 入站协议
- 监听端口
- 服务模式（普通组 / 并行组）

## 构建

```bash
git clone https://github.com/Hamster-Prime/DNS_automatic_traffic_splitting.git
cd DNS_automatic_traffic_splitting
go build -o doh-autoproxy cmd/doh-autoproxy/main.go
```

支持平台：

- Linux: `amd64` / `arm64` / `386`
- Windows: `amd64`

## 常见问题

### 为什么要配置 `bootstrap_dns`？

当上游地址本身是域名时，程序需要先解析这个域名。`bootstrap_dns` 用来完成这个步骤，避免系统 DNS 再次回到本服务形成循环依赖。

### GeoSite 未收录的域名怎么处理？

会进入双路并发查询逻辑，同时查询国内和海外上游，再根据 `GeoIP` 选择结果。

### 并行返回模式和普通模式的区别是什么？

普通模式会优先返回单个成功结果；并行返回模式会保留目标分组内多个上游的成功结果，并合并返回给下游。

### 如何确认请求走的是普通组还是并行组？

在 Web 日志中可以直接看到：

- 入站协议
- 监听端口
- 服务模式（普通组 / 并行组）

## License

本项目采用 [MIT License](LICENSE)。
