# API 出站与 TUN 排查

`CHUNKING_FAILED` 是分块写入阶段，`VECTORSTORE_WRITE_FAILED` 是向量索引阶段；先看同一次处理的服务端底层错误，再判断是数据库、模型响应还是网络问题。不要仅凭页面阶段名称认定 DocReader 或 TUN 故障。

## 不消耗模型额度的检查

把占位地址替换为当前实际配置；URL 不需要 API Key。命令只做 DNS，以及可选的 TCP/TLS 握手，不发送 HTTP 请求、文件、提示词或认证信息，不修改任何网络设置。输出只保留协议、主机、端口和 DNS 地址，隐藏 URL 路径、查询参数和代理凭据。

```bash
python3 scripts/check-api-network.py \
  --url 'https://model-api.example.com/v1' \
  --direct-url 'http://10.20.30.40:18090' \
  --filter-mode blacklist --probe tls
```

也可用 `--config /path/to/endpoints.json` 读取 JSON 列表或嵌套配置中 `base_url`、`baseURL`、`api_url`、`endpoint`、`url` 字段，不输出其他字段。应使用只有端点的最小配置；不必导出完整模型密钥配置。默认 `--probe dns`；`tcp`/`tls` 连接经过 DNS 校验的具体地址，TLS 始终验证证书及原域名。探测不使用显式 HTTP 代理，因此只代表当前直连/TUN 路径，不能证明平台认证、模型配额或显式代理可用。

`dns_status=fake_ip` 表示解析到了 `198.18.0.0/15`。平台把它判为保留地址并阻止连接，符合现有 SSRF 策略。`restricted` 表示私网、回环等非公网答案；只有明确传入 `--direct-url` 的私网或回环服务才允许握手检查，云元数据、Fake IP 等地址始终不探测。DNS、地址策略或握手失败时退出码为 1。

## 保留 TUN 的定向处理

Mihomo 的 `fake-ip-filter` 控制是否向匹配域名返回虚拟地址，与实际代理分流是两件事。只把**实际模型/解析服务的精确域名**设为真实 DNS；是否直连继续服从原路由策略。内网平台地址可另设精确 `DIRECT`。不向平台的 SSRF 白名单加入整个虚拟网段，不关闭 TLS 校验，不把供应商域名永久解析到某个 IP。[Mihomo DNS 文档](https://wiki.metacubex.one/config/dns/)

诊断输出的 `mihomo_fragment` 是需要合入现有列表的局部片段：

- `blacklist`：将域名追加到原 `fake-ip-filter`，保留已有条目。
- `rule`：将 `DOMAIN,…,real-ip` 插到原规则及 `MATCH` 之前，保留原兜底。
- `whitelist`：匹配域名反而会返回虚拟地址，**不能追加域名**。工具不生成可误用的 DNS 覆盖片段；精确匹配可从原白名单移除，宽泛匹配需要人工评审后转换成规则模式。

只有显式 `--direct-url` 生成 `DIRECT`；IP 规则使用 `/32` 或 `/128`。输出的 `tun.route-exclude-address` 同样只是精确 IP，且只在启用 `auto-route` 时有对应作用。工具不会启停 TUN、刷新订阅或改动其他应用路由。[Mihomo TUN 文档](https://wiki.metacubex.one/config/inbound/tun/)

## Clash Verge Rev 的持久化方式

不要把几条新域名直接写成 Merge 的整个 `fake-ip-filter` 或把新路由直接写成整个 `rules`，那会替换已有列表。优先在现有扩展脚本中合并数组并去重；保留原 `main(config)` 的其他逻辑及返回值。全局 Merge → 全局 Script → 订阅 Merge → 订阅 Script 按顺序执行，后置扩展仍可覆盖前面的结果。保存后用客户端重新激活配置，检查最终运行配置；直接修改临时运行 YAML 不会在下次生成时保留。应用设置还会回写接管的 TUN 字段，所以本次无需修改 `tun.enable`。[Clash Verge Rev 扩展规则](https://www.clashverge.dev/guide/extend.html)、[官方脚本说明](https://www.clashverge.dev/guide/script.html)

## 本地启动配置

`scripts/run-local-service.sh` 读取 `.private/local.env` 后，保留 `SSRF_WHITELIST_EXTRA` 并补充回环服务；同时合并 `NO_PROXY` 和 `no_proxy`，同步两个拼写。只对管理员明确受信的主机设置 SSRF 例外，不能把疑似 TUN 问题改成全网允许。

`NO_PROXY` 只影响显式 HTTP 代理选择，不会绕过操作系统层的 TUN 路由或 DNS 劫持。Go `ProxyFromEnvironment` 遵循 HTTP(S)_PROXY/NO_PROXY；HTTPX 默认读取环境，而 `trust_env=False` 的内部服务客户端刻意保持直连。平台不同客户端的设置应分别检查，不能只设置一个环境变量就认为整条链路已验证。[Go 官方说明](https://pkg.go.dev/net/http#ProxyFromEnvironment)、[HTTPX 环境变量](https://www.python-httpx.org/environment_variables/)
