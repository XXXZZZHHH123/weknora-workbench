# 手动更新部署（CentOS 7 / x86_64 / 内网）

流程：main 更新 → Python / 前端 / Go MCP 回归及部署脚本检查 → 比较每个服务的构建输入，复用未变化镜像、构建变化镜像（linux/amd64） → 发布完整的 GitHub Release → 在服务器手动运行 `bin/update.sh` 拉取最新已发布版本 → PostgreSQL 与研究数据库备份 → Compose 更新与健康检查 → 更新 current 指针。

PR 运行检查和镜像构建，不推送镜像、不发布部署版本。发布权限使用流水线自带的 GITHUB_TOKEN，服务器只保存专用的读取令牌，不需要开放公网 SSH。

首次构建 Go/Rust/BrowserSkill 和下载原生依赖会较慢。docreader 按本仓库源码构建，以包含页码和图表来源处理。PostgreSQL、Redis 使用明确的版本标签，在 GitHub 上重新发布到本项目 GHCR 下，使服务器无需访问 Docker Hub。这里只启动 PostgreSQL 检索、本地文件存储和结构化工作台；需要额外检索引擎或 SearXNG 时应扩展配置。原本的本机 Compose 和数据卷不受影响。

## 日常指令清单（当前已配置服务器）

除 SSH 登录和代理转发外，以下命令都在服务器的 `codexuser` 终端执行。项目已经完成 Docker 存储迁移与本地账户/数据迁移，不需要再次初始化。浏览器访问 `http://10.11.60.130:18090/`，使用原来的邮箱和密码。

### 登录和退出

在 Mac 终端登录；此写法沿用 `codex-linux` 的密钥配置，并关闭该次连接的自动端口转发，避免与现有代理隧道冲突：

```sh
ssh -o ClearAllForwardings=yes codex-linux
```

如果已经登录服务器但提示符是 `[root@localhost ~]#`，先切换用户：

```sh
su - codexuser
```

退出当前用户会话：

```sh
exit
```

### 手动部署最新版

代码合并到 main 后，等待 GitHub Actions 的 Workbench CI/CD 发布成功，再在服务器运行：

```sh
bash /home/codexuser/weknora-workbench/bin/update.sh
```

该命令只检查一次最新已发布版本。使用 Linux 独立代理时自动启动代理，结束后自动关闭；多个部署/Codex 调用同时运行时，等最后一个调用及其连接结束才关闭。已是最新版则提示退出；有新版本则准备镜像、备份 PostgreSQL、部署并检查健康状态。服务器没有每两分钟检查的任务。排除上一次部署失败原因后，显式重试：

```sh
bash /home/codexuser/weknora-workbench/bin/update.sh --retry-failed
```

### 查看版本、状态、日志和磁盘

```sh
# 当前成功部署的版本
readlink /home/codexuser/weknora-workbench/current
# 本项目容器；正常情况下七个服务均为 healthy
docker ps -a --filter label=com.docker.compose.project=weknora-workbench-prod
# 最近的更新日志
tail -n 100 /home/codexuser/weknora-workbench/shared/deploy.log
# 持续查看后端日志；Ctrl+C 结束查看，不停止服务
docker logs --tail 100 -f weknora-workbench-prod-app-1
# 结构化工作台日志
docker logs --tail 100 -f weknora-workbench-prod-structured-data-1
# 接口健康检查
curl -fsS http://127.0.0.1:18090/structured/health
# Docker 实际数据目录及新盘剩余空间
docker info --format '{{.DockerRootDir}}'
df -h /mnt/weknora-storage
```

### 启停、配置和数据库备份

每次 SSH 登录后，如果需要以下维护操作，先在服务器粘贴这个函数。它自动使用当前版本、服务器配置和固定项目名；不会输出 `.env` 中的密钥：

```bash
wb() {
  local wb_root=/home/codexuser/weknora-workbench
  DEPLOY_SHARED_DIR="$wb_root/shared" docker compose \
    --project-name weknora-workbench-prod \
    --env-file "$wb_root/shared/.env" \
    --env-file "$wb_root/current/release.env" \
    -f "$wb_root/current/compose.yml" "$@"
}
```

按需执行一条操作：

```sh
# 查看本项目状态
wb ps
# 停止本项目服务，保留容器和数据卷
wb stop
# 启动当前已部署版本，等待依赖及健康检查；不下载镜像
wb up -d --no-build --pull never --wait --wait-timeout 300
# 重启应用服务；不重新读取修改后的 .env
wb restart app structured-data commodity-research frontend
# 查看所有服务最近日志
wb logs --tail 100
# 编辑运行配置；不要重新生成 AES 密钥，否则原有加密凭据无法解密
vi /home/codexuser/weknora-workbench/shared/.env
# 编辑完成后，重新应用配置并检查健康状态
wb up -d --no-build --pull never --wait --wait-timeout 300
# 列出已有 PostgreSQL 备份
wb exec -T postgres ls -lh /backups
```

需要额外手动备份数据库时执行：

```sh
wb exec --user 0 -T postgres sh -c 'set -e; umask 077; backup="/backups/manual-$(date -u +%Y%m%dT%H%M%SZ).pg.dump"; pg_dump -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Fc -f "$backup.tmp"; mv "$backup.tmp" "$backup"; printf "Saved: %s\n" "$backup"'
```

这个备份仅含 PostgreSQL。结构化数据文件、原文件和 `shared/.env` 需要另行保留；数据库恢复会覆盖当前内容，应按选定备份和对应代码版本执行，不作为日常更新步骤。不要使用 `docker compose down -v` 或删除 `/mnt/weknora-storage/docker-data`。

### 调整结构化解析的时间与内存预算

结构化工作进程默认限时 300 秒、内存 768 MB。大文件首次导入若报“任务超过时间限制”，可在实际部署的 `shared/.env` 中设置，例如：

```dotenv
STRUCTURED_REVIEW_TIMEOUT_SECONDS=900
STRUCTURED_REVIEW_MEMORY_MB=768
```

当前版本的 `structured-data.environment` 必须包含这两个变量的转发；旧版本只修改 `.env` 不会生效。更新配置后，先确认没有正在处理的解析任务，再复用上面的 `wb()` 函数，仅重建结构化服务：

```sh
wb up -d --no-deps --no-build --pull never --force-recreate structured-data
```

这会保留原数据卷。`restart` 不会重新读取修改后的环境变量；不要执行 `down -v`。`wb()` 已固定原生产项目名、两份环境文件和当前版本的 Compose 路径，不能在其他目录执行裸 `docker compose`，以免操作另一套服务。

重建前后可核对运行进程的实际值；下面的命令只打印这两个参数，不输出完整环境或密钥：

```sh
wb exec -T structured-data python -c 'from pathlib import Path; rows=Path("/proc/1/environ").read_bytes().split(b"\0"); keys=("STRUCTURED_REVIEW_TIMEOUT_SECONDS","STRUCTURED_REVIEW_MEMORY_MB"); print({k:next((v.split(b"=",1)[1].decode() for v in rows if v.startswith(k.encode()+b"=")),"<unset>") for k in keys})'
```

`<unset>` 表示未传入，对应程序默认值。`/structured/health` 不返回工作进程预算，健康检查通过不能证明新的时限已生效。

### 维护时禁止更新、回退版本

设置维护标记只阻止更新脚本，不停止正在运行的服务：

```sh
touch /home/codexuser/weknora-workbench/shared/PAUSED
# 维护完成后允许手动更新
rm -f /home/codexuser/weknora-workbench/shared/PAUSED
```

回退前先核对旧代码与当前数据库结构兼容。下面的命令重新部署 previous 版本，并执行数据库备份和健康检查；不会还原数据库。维护标记不会阻止直接运行 deploy.sh：

```sh
touch /home/codexuser/weknora-workbench/shared/PAUSED
previous_release=$(readlink /home/codexuser/weknora-workbench/previous)
test -f "$previous_release/deploy.sh" && bash "$previous_release/deploy.sh" \
  /home/codexuser/weknora-workbench "$(basename "$previous_release")"
```

确认回退后的服务正常后，移除 `shared/PAUSED`。下一次运行 `update.sh` 仍会部署 GitHub 最新版本，所以原问题未修复时不要再次运行更新命令。

### Linux 独立代理（替代 Mac 转发）

服务器可以独立运行 Mihomo，导入现有 Clash/Mihomo HTTPS 订阅。固定入口是 `http://127.0.0.1:17891`，控制器仅监听 `127.0.0.1:17991` 并使用随机访问密钥。订阅只提供节点，订阅中的系统/TUN/DNS 设置不作为运行配置导入。

代理使用一个独立的 `weknora-egress-proxy` 容器，复用服务器已有结构化服务镜像作为基础环境，挂载经过 SHA256 校验的 Mihomo v1.19.32 compatible 二进制。容器以普通用户运行、文件系统只读、没有 Linux capabilities，未授予 NET_ADMIN；host 网络模式避免增加 Docker 网桥或端口转发规则。TUN、透明代理和 DNS 监听均关闭，不修改默认路由、系统 DNS、Docker daemon、全局环境变量或其他应用配置。内核已在 CentOS 7 / Linux 3.10 上实测启动，回环代理 HTTPS 请求通过；安装前后 IPv4/IPv6 路由配置、系统 DNS 和原有健康应用容器没有变化。

首次安装把 `linux-proxy.sh`、`linux-proxy-config.py`、`proxy-session.sh`、`with-proxy.sh` 放在 `~/weknora-workbench/setup/`，bootstrap 也会把这四个文件安装到 `bin/`。安装脚本使用 GitHub 官方 Release 并校验固定 SHA256；服务器不能直连下载时，可以在 Mac 下载并校验后一次性传送二进制及校验文件到 `~/.local/share/weknora-proxy/runtime/`。这只是安装文件传输，后续代理运行不依赖 Mac。

```sh
# 已安装二进制时会验证本地校验文件并直接退出，不重复下载
bash ~/weknora-workbench/setup/linux-proxy.sh install
# 在服务器 codexuser 的交互终端隐藏输入订阅，链接不进入命令参数或历史
bash ~/weknora-workbench/setup/linux-proxy.sh configure
```

`configure` 保存私有配置到 `~/.local/share/weknora-proxy/config/config.json`（目录 700、文件 600），校验后启动独立容器；只有 GitHub API 返回 200、GHCR 返回正常认证挑战 401，才把部署脚本的下载代理改成 17891 并继续使用 regctl。不会重新部署或重启应用。本项目日常 `wb stop` 不会停止这个独立代理。

```sh
# 查看运行状态、当前实际节点及运行期累计字节数
bash ~/weknora-workbench/setup/linux-proxy.sh status
# 列出可选节点名称；不会输出订阅链接或节点密码
bash ~/weknora-workbench/setup/linux-proxy.sh nodes
# 测试 GitHub/GHCR/OpenAI 网络连接，不发送模型请求
bash ~/weknora-workbench/setup/linux-proxy.sh check
# 测量全部节点 HTTPS 延迟，按毫秒从低到高排列
bash ~/weknora-workbench/setup/linux-proxy.sh latency
# 需要时按 nodes 给出的真实名称选节点
bash ~/weknora-workbench/setup/linux-proxy.sh select '替换为实际节点名称'
# 网络验证通过后，允许更新脚本使用 Linux 代理
bash ~/weknora-workbench/setup/linux-proxy.sh activate
```

`check`、`nodes`、`latency`、`select`、`activate` 自动启动代理，并在调用结束且没有其他调用时关闭；`status` 仅查看状态。测速使用 HTTPS 连通性测试地址，`timeout` 表示未在 5 秒内完成；低延迟不保证 OpenAI 可用，切换后仍需 `check`。`select 'AUTO'` 恢复自动选择低延迟节点。

GitHub 匿名 API 请求按出口 IP 共享每小时 60 次额度，`403` 加 `X-RateLimit-Remaining: 0` 表示额度耗尽，不是代理断线。检查脚本使用服务器已有 `shared/github-token`，令牌不进入命令参数或输出；如果仍遇到额度耗尽，会继续检查其他站点并明确报告限流。无效令牌或其他 403 仍视为检查失败。[GitHub 官方额度说明](https://docs.github.com/en/rest/using-the-rest-api/rate-limits-for-the-rest-api)。

OpenAI API 无凭据返回 401 仅证明该 HTTPS 请求可达，不证明 Codex 已登录或模型会话可用。CentOS 7 的 curl 7.29/NSS 对 OpenAI 可能出现 TLS EOF，检查脚本使用已有 Python 3.12/OpenSSL 运行时验证，保持宿主机 TLS 包不变。若现代客户端仍返回 403/连接失败，需核对节点对目标服务的访问。本机已安装并验证官方 Codex CLI 0.162.1 的 Linux x86_64 musl 二进制，避免依赖旧 glibc；安装包来自官方 GitHub Release，并核对其 SHA256。2026-10-14 验收：GitHub API 200、GHCR 401、完整私有 Redis 镜像下载成功、OpenAI API 401、登录发现接口 200、Codex 后端 GET 返回 405；服务器已有 ChatGPT 登录状态，在隔离的只读临时目录运行最小 Codex 请求返回 `OK`。没有读取或修改项目文件。

按进程使用代理，变量只传递给指定命令及其子进程，普通 SSH 会话和其他应用的环境不改变：

```sh
# 首次安装；当前服务器已经完成
for script in linux-proxy.sh linux-proxy-config.py proxy-session.sh with-proxy.sh; do
  install -m 700 ~/weknora-workbench/setup/"$script" ~/weknora-workbench/bin/"$script"
done
# 普通 curl 不受影响；显式运行的这个 curl 使用代理
bash ~/weknora-workbench/bin/with-proxy.sh curl -I https://github.com
# 新环境没有登录状态时才需要登录；当前服务器已经登录并验证
bash ~/weknora-workbench/bin/with-proxy.sh codex login --device-auth
# 当前服务器直接运行
bash ~/weknora-workbench/bin/with-proxy.sh codex
```

直接 `docker pull` 的联网主体是 daemon，不能依靠这个进程包装脚本改变其网络；本项目继续用 regctl 下载后导入镜像，保留现有 Docker daemon 设置。代理故障时只影响显式使用它的工具；此处没有为其他应用启用代理。

代理现在按需运行：`update.sh` 或 `with-proxy.sh codex` 自动开启，命令结束、出错或中断后释放使用权，最后一个调用退出且连接结束后自动关闭。文件锁协调同时运行的调用；一个本地守护进程只检查使用权及回环连接，处理包装进程被强制结束的情况，不查询 GitHub、订阅或远程发布。现有长期连接也会阻止自动关闭。直接运行 `codex` 不经过自动开关入口。

Docker 已设置开机启动，代理容器改为 `on-failure:3`，仅在运行期间异常退出时重试；服务器重启后代理保持关闭，下一次部署或 Codex 调用自动启动，无需再次导入订阅。GitHub Actions 仍只发布版本，服务器保持手动部署。[Docker 官方重启策略](https://docs.docker.com/engine/containers/start-containers-automatically/)。

日常临时关闭/开启，保留 Linux 代理的配置和节点选择：

```sh
bash ~/weknora-workbench/setup/linux-proxy.sh stop
# 只有需要在包装脚本外使用代理时才手动开启
bash ~/weknora-workbench/setup/linux-proxy.sh start
bash ~/weknora-workbench/setup/linux-proxy.sh check
```

代理只接收显式使用它的命令，不会把其他应用的普通流量转过去。运行期间仍有少量节点健康检查（配置间隔 600 秒）和订阅刷新（86400 秒），自动关闭后不产生这些流量。status 输出是运行期计数，重启可能清零；精确计费以订阅服务商统计为准。已有部署继续运行。`stop` 遇到仍在运行的包装命令会拒绝，避免中断 Codex 或部署。无需为了临时关闭而执行 restore-deploy。

只有彻底撤销 Linux 代理的部署配置时才运行下面的恢复命令：

```sh
bash ~/weknora-workbench/setup/linux-proxy.sh restore-deploy
bash ~/weknora-workbench/setup/linux-proxy.sh stop
```

恢复的旧配置可能仍指向 Mac 转发端口；因此这是撤销新配置，不代表旧代理隧道自动恢复。订阅、节点缓存、控制器密钥都只保存在服务器私有目录，不提交到 Git。

实现依据：[Mihomo 代理端口](https://wiki.metacubex.one/config/inbound/port/)、[节点订阅集合](https://wiki.metacubex.one/config/proxy-providers/)、[TUN 对路由及防火墙的影响](https://wiki.metacubex.one/config/inbound/tun/)、[官方发布包](https://github.com/MetaCubeX/mihomo/releases/tag/v1.19.32)。Codex 登录步骤依据 [OpenAI 官方无界面设备登录说明](https://learn.chatgpt.com/docs/auth#login-on-headless-devices)。

### 旧方式：在 Mac 建立转发（保留作为备用）

当前配置使用服务器 `127.0.0.1:17890` 作为下载代理，并转发到 Mac 的 `127.0.0.1:1082`。如果代理隧道已在线，不需重复建立。若离线，先启动 Mac 上监听 1082 的代理软件，再在一个独立的 Mac 终端运行并保持连接：

```sh
ssh -o ClearAllForwardings=yes -o ExitOnForwardFailure=yes \
  -N -R 127.0.0.1:17890:127.0.0.1:1082 codex-linux
```

另开 Mac 终端按上面的登录命令连接服务器，检查代理：

```sh
curl --max-time 15 --proxy http://127.0.0.1:17890 https://api.github.com
```

返回 GitHub JSON 说明代理请求可用；无法连接时先恢复代理再部署。服务已启动后，本地 Docker 可以保持关闭；上述 SSH 隧道与本地 Docker 容器无关。

## 1. 服务器准备（只需一次）

已知服务器：SSH 别名 `codex-linux`，内网地址 `10.11.60.130`，用户 `codexuser`，CentOS 7，Docker 24.0.7。部署检查已确认服务器可直连 GitHub API、Release 下载和 GHCR 清单；完整镜像下载需要代理，Docker Hub 超时。

`17890` 当前来自 SSH 转发，只作为下载工具时的可选代理。控制 API 曾验证直连可用，但完整镜像下载需代理；正式发布应在 `shared/poller.json` 中设置持续可用的代理，避免依赖临时 SSH 会话。查询 IP 得到的代理出口不能用作服务器公网 SSH 地址。

新服务器若部署用户无权访问 Docker，由 root 执行（本次服务器已完成）：

```sh
groupadd -f docker
usermod -aG docker codexuser
chgrp docker /var/run/docker.sock
chmod 660 /var/run/docker.sock
```

重新建立 SSH 会话。Docker 的 socket 默认使用 docker 组；管理员如有自定义配置，应确保重启后仍使用该组。Docker 组能控制宿主机，应只授予部署用户。

将 `deploy/` 下的脚本和 `.env.example` 放在服务器 `~/weknora-workbench/setup/`。以 codexuser 执行：

```sh
bash ~/weknora-workbench/setup/install-tools.sh http://127.0.0.1:17890
bash ~/weknora-workbench/setup/bootstrap.sh \
  /home/codexuser/weknora-workbench XXXZZZHHH123/weknora-workbench
```

工具安装器验证 SHA256，安装 Compose v2.39.2、jq 1.7.1 和 regctl 0.8.3 到用户目录。bootstrap 一次生成数据库、Redis、JWT、AES 和签名密钥，再次执行保留已有配置。服务器不需要 Node、Go 或 Python 3。

实测服务器命令行可以读取 GHCR 清单，但 Docker daemon 拉取经常连接超时。可在 `shared/poller.json` 设置 `image_transport: "regctl"` 和 `proxy: "http://127.0.0.1:17890"`，通过用户态客户端下载并导入 Docker；不重启 daemon，也不修改其他项目的代理。所有镜像导入成功后才更新容器，Compose 使用 `--pull never`。健康网络环境可保留默认 `image_transport: "docker"`。17890 若来自 Mac 的 SSH 反向转发，运行更新脚本时需要该连接和 Mac 代理在线；也可使用服务器常驻代理。

## Docker 存储维护（需管理员维护窗口）

当前 Docker 数据目录所在 `/sysvol` 已满；新盘 `/dev/vdb1` 已有 XFS ftype=1 文件系统，挂载到 `/mnt/weknora-storage`。Docker 现已切换到 `/mnt/weknora-storage/docker-data`，旧目录保留作为维护备份。不要格式化这块盘，它已经有其他数据。

`migrate-docker-storage.sh` 默认只检查容量并展示计划；只有显式加 `--migrate` 才会修改挂载/配置、暂停现有容器并切换数据目录。脚本复制到新建的 `docker-data` 目录，保留旧目录和配置备份，加入持久挂载及 Docker 挂载依赖；不会删除原数据或触碰新盘其他目录。仅在管理员批准停机时由 root 执行，不属于日常发布流程。

```sh
# 只检查
bash /home/codexuser/weknora-workbench/setup/migrate-docker-storage.sh /mnt/weknora-storage
# 批准维护后执行
bash /home/codexuser/weknora-workbench/setup/migrate-docker-storage.sh /mnt/weknora-storage --migrate
```

拷贝时间取决于旧 Docker 数据量。预复制期间原容器继续运行，最后同步期间会停止并重新启动原先运行的容器。依赖 Docker 的 systemd 服务会按原服务恢复，避免带 `--rm` 的容器在停止后无法按旧 ID 启动；未找到管理服务的自动删除容器会在迁移前拒绝执行。迁移状态目录中的容器 inspect 快照可能含运行凭据，应保持 root-only 权限。失败时先核对输出中的状态目录，原配置备份为 `daemon.json.before`；恢复原数据目录前需要停 Docker、恢复配置并重新启动，再核对原有业务。脚本的 ShellCheck 已验证，真实数据迁移仍须管理员在服务器验收。

## 2. 私有仓库与镜像读取凭据

服务器建议使用独立的 classic PAT：`repo`（classic PAT 读取私有仓库所需，范围较宽）及 `read:packages`，不要使用开发者的日常推送令牌。也可以分别使用 Contents:read 的 fine-grained PAT 读取发布包，以及另一个 read:packages 的 classic PAT 登录 GHCR。令牌还需要拥有该仓库及七个镜像包的读取权限；组织 SSO 需按组织要求授权。

令牌不要放进项目 `.env`、Git、命令参数或聊天。下面在服务器交互终端执行，输入不会回显：

```sh
umask 077
read -rs -p 'Server GitHub read token: ' server_token; echo
printf '%s\n' "$server_token" > ~/weknora-workbench/shared/github-token
chmod 600 ~/weknora-workbench/shared/github-token
printf '%s' "$server_token" | docker login ghcr.io -u XXXZZZHHH123 --password-stdin
unset server_token
```

若 API 令牌与镜像令牌分开，登录时输入镜像读取令牌。更新脚本的令牌只用于 GitHub API，不进入业务容器；下载跳转时移除认证头。Docker 在 `~/.docker/config.json` 保存登录凭据，保留该文件的用户访问权限。

## 3. 业务配置与首次启动

编辑 `~/weknora-workbench/shared/.env`。内网直接访问可设置 `FRONTEND_BIND=0.0.0.0`；默认 `127.0.0.1:18080` 供宿主机 HTTPS 反向代理或 SSH 转发访问。只发布前端端口，数据库、后端和结构化服务都留在容器网络内。

本次服务器使用 `FRONTEND_BIND=0.0.0.0`、`FRONTEND_PORT=18090`、`STRUCTURED_PUBLIC_ORIGIN=http://10.11.60.130:18090`，访问地址为 `http://10.11.60.130:18090/`。18080 已被原有 Tomcat 使用。更换访问 IP、域名或 HTTPS 时同步修改公开 origin；只接受一个明确的 HTTP(S) origin，不使用通配符，认证和同源检查继续保留。2026-10-09 首次部署已验证六个服务健康、浏览器登录页正常、API 未登录返回 401、结构化健康接口返回 JSON，并验证 PostgreSQL 备份归档可读取。

注册首个账号后关闭注册（`DISABLE_REGISTRATION=true`），确认真实租户 ID 并修改 `STRUCTURED_TENANT_ID`。在 WeKnora 和工作台页面分别配置聊天模型、字段理解模型。新部署默认没有任何本机账号、知识库或表格数据。

首次运行前检查 `shared/.env` 不含空白密钥。main 首次成功构建后运行：

```sh
bash ~/weknora-workbench/bin/update.sh
```

以后需要更新时执行同一条命令。脚本只查询一次最新 Release；当前已是最新版时提示并退出，不重新下载镜像或重启服务。正常更新会校验发布包、准备所需镜像、备份 PostgreSQL、启动服务并检查健康状态，输出同时保存到 `shared/deploy.log`。

服务器不设置定时获取任务；2026-10-09 已移除原来的每两分钟 cron。GitHub Actions 仍自动测试、构建并发布版本，但服务器只有在执行 `update.sh` 时才更新。安装脚本不会添加 cron；已有服务器切换时需移除对应旧任务，保留其他项目的定时任务。

发布前检查 Docker 数据目录所在文件系统至少有 15 GiB 可用空间，建议预留 20 GiB 以上。2026-10-09 初次检查时 `/sysvol/docker_image_var` 所在分区已满；现已迁移到 `/mnt/weknora-storage/docker-data`。迁移期间曾使用 `shared/PAUSED` 阻止发布，存储准备完成后解除。

数据库、Redis、原文件、结构化表格均使用 Docker 命名卷，随 Docker 数据目录位于新盘；数据库备份也保存在新盘的 Docker 命名卷；发布脚本、运行配置和令牌位于 `~/weknora-workbench/`。

每次发布从镜像中启动代码，不挂载源码。下载发布包并验证 SHA256 及归档成员后才解压；所有镜像准备成功才更新服务。发布编号使用整项目提交号、流水线编号和运行次数，`release.env` 分别记录七个服务的镜像引用和构建输入哈希。源码、依赖声明、Dockerfile 或基础镜像版本没有变化时复用原镜像；文档或测试修改不会重建运行镜像。服务器发现镜像已存在便跳过下载。复用前 CI 检查镜像仍可读取，缺失时重新构建；初次迁移到此规则时复用已经在服务器运行的 `3652036` 镜像。镜像中的版本信息表示该服务上次实际构建的提交。

基础镜像版本集中在 `deploy/image-builds.json`，变更版本会重建相应服务。手动运行工作流可勾选 `force_rebuild` 强制重建全部镜像。PR 保留七个镜像的构建验证，不推送。只有测试、镜像构建或复用检查、发布步骤全部成功的版本才会进入服务器。不要手动把其他类型的 Release 标记为 Latest，以免阻断生产发布发现。

首次健康检查确认前端、受保护 API 返回 401、结构化 `/health` 返回 JSON；这不等于已验证真实模型问答。首次配置账号及模型后，还需检查页面登录、工作台上传/查询、歧义选择和证据显示。

结构化 MCP 智能体需要一次性注册，参考 `structured-data/docs/WeKnora接入.md`。服务 URL 使用 `http://structured-data:18082/mcp`，token 位于 structured-data 容器的 `/data/service-token`。注册脚本默认只读预览；应用注册时用 `--apply`，以后发布不重复注册或覆盖智能体配置。

## 4. 状态、暂停与恢复

```sh
readlink ~/weknora-workbench/current
tail -n 50 ~/weknora-workbench/shared/deploy.log
docker ps --filter label=com.docker.compose.project=weknora-workbench-prod
```

维护时可以暂时禁止更新脚本执行（不停止服务）：

```sh
touch ~/weknora-workbench/shared/PAUSED
# 维护完成后允许手动更新
rm ~/weknora-workbench/shared/PAUSED
```

健康检查失败不移动 current，也不自动删除/还原数据卷；部分容器可能已运行新镜像，数据库迁移也可能已执行。失败版本会记录到 `shared/failed-release`，下次手动运行会报告该版本曾经失败并返回非零退出码。查看容器日志、修复原因后可显式重试：

```sh
bash ~/weknora-workbench/bin/update.sh --retry-failed
```

每次升级先将 PostgreSQL 保存到 `weknora-workbench-prod_postgres-backups` 命名卷（postgres 容器的 `/backups/*.pg.dump`）；备份失败则中止发布。需要另行备份 `shared/.env` 以及命名卷中的结构化数据和原文件等数据（研究数据卷已在升级前自动归档），并定期清理旧备份/镜像/版本。数据库备份发生时现有服务可能继续写入，不是全部业务数据的同一时点快照。

回到旧镜像前先设置 `shared/PAUSED`，核对旧代码与当前数据库结构兼容性，再运行 previous 目录下的 deploy.sh。脚本仅回退镜像/Compose，不还原数据库，数据库恢复须停写并从选定备份恢复。不要使用 `docker compose down -v`。服务器卷固定使用 `weknora-workbench-prod` 项目名，不能随版本改变。

## 5. 验证范围

CI 检查结构化服务和商品研究测试、研究空库迁移及模型对齐、前端类型与测试、Go MCP 回归、ShellCheck、部署状态与失败路径测试、Compose 配置以及七个镜像构建；用户态镜像导入及失败中止也有回归测试。没有访问生产密钥、生产数据或调用收费模型。只有服务器实际拉取、启动并通过健康检查，才证明该版本可以在这台 CentOS 7 主机上运行；源码检查不能替代该验证。

GitHub Actions 绿色表示发布包已准备好；服务器部署状态目前记录在 `shared/deploy.log`，未回写 GitHub Deployments。代理离线或令牌失效时更新脚本报错，服务器保留当前版本；恢复连接后重新手动运行更新脚本。

实现依据：[GitHub 镜像发布](https://docs.github.com/en/actions/tutorials/publish-packages/publish-docker-images)、[GHCR 认证](https://docs.github.com/en/packages/working-with-the-container-registry/working-with-the-container-registry)、[Release API](https://docs.github.com/en/rest/releases/releases)、[Compose 等待健康检查](https://docs.docker.com/reference/cli/docker/compose/up/)。

用户态镜像导入依据：[regctl image export](https://regclient.org/cli/regctl/image/export/)。


## 6. 商品研究服务（第一版合仓）

新增第七个服务 `commodity-research`，Python/FastAPI 监听内部 8765。研究入口是 `/platform/commodity-research`，Nginx 将同源 `/research/` 转给研究服务。研究员身份、当前空间由 WeKnora 登录校验；新空间在首次访问时创建独立数据库。公开 `/research/healthz` 只检查进程，不创建数据库、不返回研究数据；部署另外检查未登录研究 API 必须返回 401，研究 CSS 与 JS 必须返回 200 且类型正确，防止页面有 HTML 但样式和按钮失效。

研究持久数据卷是 `weknora-workbench-prod_commodity-research`，目录为 `/data/workspaces/<tenant-id>/tin.db`，其余来源原件、暂存与导出也放 `/data`。第二阶段由研究服务内部按已创建空间的计划更新业务数据，默认北京时间每天 08:00、16:30，首页可启停、改时间和手动更新。`TIN_REFRESH_SCHEDULER_ENABLED=false` 可关闭整个实例的自动取数。不会复制旧 CRP systemd 定时器，不运行默认研究员 `me` 的旧 `daily`，不建立代码轮询或自动部署任务。

服务调用密钥单独保存在 `shared/research-service-token`，启动/更新时只在不存在时生成，权限 0600。app、研究服务与结构化服务共用路径绑定、账号/空间绑定的签名读取密钥；研究 entrypoint 复制到私有目录并降权运行。`STRUCTURED_DATA_URL` 与 `TIN_STRUCTURED_DATA_URL` 固定指向内部结构化服务。这个密钥必须与 `shared/.env` 一并安全备份；不要在日志或聊天中打印。历史数据导入和研究智能体配置不随服务器每次更新重复执行。

```sh
# 服务器：健康与日志
curl -fsS http://127.0.0.1:18090/research/healthz
wb logs --tail 100 commodity-research
# 查看研究数据归档（每次升级前）
ls -lh /home/codexuser/weknora-workbench/shared/backups/*.research.tar.gz
```

升级前对每个 SQLite 数据库使用在线 backup API，并执行完整性检查，再归档数据目录；已提交在 WAL 中的数据也会保存，归档不包含 WAL/SHM 文件或服务密钥。归档写入失败就中止更新，成功文件位于 `shared/backups/<time>-<release>.research.tar.gz`。多个数据库及原文件分别在备份过程中读取，不是跨服务的同一时点快照；需要数据库与导入文件严格同一时点时，先进入维护并停写后另做完整备份。

恢复时先 `touch shared/PAUSED` 并 `wb stop commodity-research`。先把选定归档恢复到一个**新的卷**检查，不覆盖原卷。以下命令使用当前已经下载的研究镜像和 Docker，服务器无需 Python 环境：

```bash
# 服务器：在同一终端中填写已确认的归档绝对路径和新卷名。
research_archive=/home/codexuser/weknora-workbench/shared/backups/REPLACE.research.tar.gz
research_restore_volume=weknora-research-restore-REPLACE
research_image="$(wb config --format json | jq -r '.services["commodity-research"].image')"
docker volume create "$research_restore_volume"
docker run --rm -i --entrypoint sh -v "$research_restore_volume:/data" "$research_image" \
  -c 'tar -xzf - -C /data && chown -R commodity:commodity /data' < "$research_archive"
# 检查所有恢复后的数据库。
docker run --rm --entrypoint python -v "$research_restore_volume:/data:ro" "$research_image" -c '
from pathlib import Path
import sqlite3
for path in Path("/data/workspaces").glob("*/tin.db"):
    with sqlite3.connect(path.as_uri()+"?mode=ro", uri=True) as db:
        assert db.execute("PRAGMA integrity_check").fetchone()[0] == "ok", str(path)
        print(path.parent.name, "ok")
'
```

核对恢复记录和对应代码的 Alembic 版本后，另行确认将研究服务的数据卷切到已恢复的新卷；原卷继续保留。`previous/deploy.sh` 只回退镜像，不恢复数据。不要使用 `down -v`，也不要把有写入的 SQLite 主文件单独复制当作备份。

本机需先构建研究镜像：`docker compose -p weknora-research -f commodity-research/compose.yml build`，前端仍用 `bash structured-data/scripts/build_frontend.sh`，再执行根目录 `bash start.sh`。本机研究数据绑定 `commodity-research/data`，服务密钥生成一次；启动脚本不会注册或覆盖智能体。生产是否可运行，仍以服务器手动更新和实际健康检查为准。
