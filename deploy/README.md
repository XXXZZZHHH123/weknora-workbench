# 手动更新部署（CentOS 7 / x86_64 / 内网）

流程：main 更新 → Python / 前端 / Go MCP 回归及部署脚本检查 → 比较每个服务的构建输入，复用未变化镜像、构建变化镜像（linux/amd64） → 发布完整的 GitHub Release → 在服务器手动运行 `bin/update.sh` 拉取最新已发布版本 → PostgreSQL 与研究数据库备份 → Compose 更新与健康检查 → 更新 current 指针。

PR 运行检查和镜像构建，不推送镜像、不发布部署版本。发布权限使用流水线自带的 GITHUB_TOKEN，服务器只保存专用的读取令牌，不需要开放公网 SSH。

首次构建 Go/Rust/BrowserSkill 和下载原生依赖会较慢。上游 docreader、PostgreSQL、Redis 使用明确的版本标签，在 GitHub 上重新发布到本项目 GHCR 下，使服务器无需访问 Docker Hub。这里只启动 PostgreSQL 检索、本地文件存储和结构化工作台；需要额外检索引擎或 SearXNG 时应扩展配置。原本的本机 Compose 和数据卷不受影响。

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

该命令只检查一次最新已发布版本。已是最新版则提示退出；有新版本则准备镜像、备份 PostgreSQL、部署并检查健康状态。服务器没有每两分钟检查的任务。排除上一次部署失败原因后，显式重试：

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

### 下载需要代理时：在 Mac 建立转发

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
