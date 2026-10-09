# 自动部署（CentOS 7 / x86_64 / 内网）

流程：main 更新 → Python / 前端 / Go MCP 回归及部署脚本检查 → 在 GitHub 构建三个应用镜像并镜像三个依赖服务到 GHCR（linux/amd64） → 发布完整的 GitHub Release → 服务器每两分钟拉取新版本 → PostgreSQL 备份 → Compose 更新与健康检查 → 更新 current 指针。

PR 运行检查和镜像构建，不推送镜像、不发布部署版本。发布权限使用流水线自带的 GITHUB_TOKEN，服务器只保存专用的读取令牌，不需要开放公网 SSH。

首次构建 Go/Rust/BrowserSkill 和下载原生依赖会较慢。上游 docreader、PostgreSQL、Redis 使用明确的版本标签，在 GitHub 上重新发布到本项目 GHCR 下，使服务器无需访问 Docker Hub。这里只启动 PostgreSQL 检索、本地文件存储和结构化工作台；需要额外检索引擎或 SearXNG 时应扩展配置。原本的本机 Compose 和数据卷不受影响。

## 1. 服务器准备（只需一次）

已知服务器：SSH 别名 `codex-linux`，内网地址 `10.11.60.130`，用户 `codexuser`，CentOS 7，Docker 24.0.7。部署检查已确认服务器可直连 GitHub API、Release 下载和 GHCR；Docker Hub 超时。

`17890` 当前来自 SSH 转发，只作为下载工具时的可选代理。正式轮询使用直连，避免依赖 Mac 的 SSH 会话；若网络策略变化，可在 `shared/poller.json` 中修改代理。查询 IP 得到的代理出口不能用作服务器公网 SSH 地址。

root 执行（目前 socket 属于 root:root，codexuser 无权使用 Docker）：

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

工具安装器验证官方 SHA256，安装 Compose v2.39.2 和 jq 1.7.1 到用户目录。bootstrap 一次生成数据库、Redis、JWT、AES 和签名密钥，再次执行保留已有配置。服务器不需要 Node、Go 或 Python 3。

实测本服务器可直连 GHCR，Docker Hub 拉取会超时，Docker daemon 尚无代理。本流程所有运行镜像均来自 GHCR，因此无需为此次部署重启 Docker 或修改 daemon 的全局代理。若其他环境连 GHCR 也需要代理，管理员应另行配置 Docker daemon 的代理；它独立于 poller 的 curl 代理。

## 2. 私有仓库与镜像读取凭据

服务器建议使用独立的 classic PAT：`repo`（classic PAT 读取私有仓库所需，范围较宽）及 `read:packages`，不要使用开发者的日常推送令牌。也可以分别使用 Contents:read 的 fine-grained PAT 读取发布包，以及另一个 read:packages 的 classic PAT 登录 GHCR。令牌还需要拥有该仓库及六个镜像包的读取权限；组织 SSO 需按组织要求授权。

令牌不要放进项目 `.env`、Git、命令参数或聊天。下面在服务器交互终端执行，输入不会回显：

```sh
umask 077
read -rs -p 'Server GitHub read token: ' server_token; echo
printf '%s\n' "$server_token" > ~/weknora-workbench/shared/github-token
chmod 600 ~/weknora-workbench/shared/github-token
printf '%s' "$server_token" | docker login ghcr.io -u XXXZZZHHH123 --password-stdin
unset server_token
```

若 API 令牌与镜像令牌分开，登录时输入镜像读取令牌。poller 的令牌只用于 GitHub API，不进入业务容器；下载跳转时移除认证头。Docker 在 `~/.docker/config.json` 保存登录凭据，保留该文件的用户访问权限。

## 3. 业务配置与首次启动

编辑 `~/weknora-workbench/shared/.env`。内网直接访问可设置 `FRONTEND_BIND=0.0.0.0`；默认 `127.0.0.1:18080` 供宿主机 HTTPS 反向代理或 SSH 转发访问。只发布前端端口，数据库、后端和结构化服务都留在容器网络内。

注册首个账号后关闭注册（`DISABLE_REGISTRATION=true`），确认真实租户 ID 并修改 `STRUCTURED_TENANT_ID`。在 WeKnora 和工作台页面分别配置聊天模型、字段理解模型。新部署默认没有任何本机账号、知识库或表格数据。

首次运行前检查 `shared/.env` 不含空白密钥。main 首次成功构建后运行：

```sh
bash ~/weknora-workbench/bin/poll.sh /home/codexuser/weknora-workbench
```

确认成功后，用 `crontab -e` 添加一条（仅添加一次）：

```cron
*/2 * * * * /bin/bash /home/codexuser/weknora-workbench/bin/poll.sh /home/codexuser/weknora-workbench >> /home/codexuser/weknora-workbench/shared/deploy.log 2>&1
```

发布前检查 Docker 数据目录所在文件系统至少有 15 GiB 可用空间，建议预留 20 GiB 以上。2026-10-09 检查时 Docker 数据目录在 `/sysvol/docker_image_var`，`/sysvol` 已满，因此首次启动暂时使用 `shared/PAUSED` 阻止轮询，待存储准备完成后解除。

每次发布从镜像中启动代码，不挂载源码。下载发布包并验证 SHA256 及归档成员后才解压；所有镜像拉取成功才更新服务。镜像使用完整提交号、流水线编号和运行次数组成的独立标签。只有测试、镜像构建和发布步骤全部成功的版本才会进入服务器。不要手动把其他类型的 Release 标记为 Latest，以免阻断生产发布发现。

首次健康检查确认前端、受保护 API 返回 401、结构化 `/health` 返回 JSON；这不等于已验证真实模型问答。首次配置账号及模型后，还需检查页面登录、工作台上传/查询、歧义选择和证据显示。

结构化 MCP 智能体需要一次性注册，参考 `structured-data/docs/WeKnora接入.md`。服务 URL 使用 `http://structured-data:18082/mcp`，token 位于 `shared/structured-data/service-token`。注册脚本默认只读预览；应用注册时用 `--apply`，以后发布不重复注册或覆盖智能体配置。

## 4. 状态、暂停与恢复

```sh
readlink ~/weknora-workbench/current
tail -n 50 ~/weknora-workbench/shared/deploy.log
docker ps --filter label=com.docker.compose.project=weknora-workbench-prod
```

暂时停止自动更新（不停止服务）：

```sh
touch ~/weknora-workbench/shared/PAUSED
# 恢复轮询
rm ~/weknora-workbench/shared/PAUSED
```

健康检查失败不移动 current，也不自动删除/还原数据卷；部分容器可能已运行新镜像，数据库迁移也可能已执行。失败版本会记录到 `shared/failed-release`，避免每两分钟重复重启。查看容器日志、修复原因后可显式重试：

```sh
bash ~/weknora-workbench/bin/poll.sh /home/codexuser/weknora-workbench --retry-failed
```

每次升级先将 PostgreSQL 保存到 `shared/backups/*.pg.dump`；备份失败则中止发布。需要另行备份 `shared/.env`、`shared/structured-data` 和命名卷中的原文件等数据，并定期清理旧备份/镜像/版本。数据库备份发生时现有服务可能继续写入，不是全部业务数据的同一时点快照。

回到旧镜像前先暂停轮询，核对旧代码与当前数据库结构兼容性，再运行 previous 目录下的 deploy.sh。脚本仅回退镜像/Compose，不还原数据库，数据库恢复须停写并从选定备份恢复。不要使用 `docker compose down -v`。服务器卷固定使用 `weknora-workbench-prod` 项目名，不能随版本改变。

## 5. 验证范围

CI 检查结构化服务测试、前端类型与测试、Go MCP 回归、ShellCheck、部署状态与失败路径测试、Compose 配置以及六个镜像构建。没有访问生产密钥、生产数据或调用收费模型。只有服务器实际拉取、启动并通过健康检查，才证明该版本可以在这台 CentOS 7 主机上运行；源码检查不能替代该验证。

GitHub Actions 绿色表示发布包已准备好；服务器部署状态目前记录在 `shared/deploy.log`，未回写 GitHub Deployments。代理离线或令牌失效时服务器保留当前版本，恢复连接后继续检查新版本。

实现依据：[GitHub 镜像发布](https://docs.github.com/en/actions/tutorials/publish-packages/publish-docker-images)、[GHCR 认证](https://docs.github.com/en/packages/working-with-the-container-registry/working-with-the-container-registry)、[Release API](https://docs.github.com/en/rest/releases/releases)、[Compose 等待健康检查](https://docs.docker.com/reference/cli/docker/compose/up/)。
