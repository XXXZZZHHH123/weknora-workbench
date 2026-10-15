# 开发与本地运行

本文件集中维护开发环境信息。项目介绍见[根 README](../README.md)，生产环境操作见[服务器运维](../deploy/README.md)。

## 环境与配置

各组件的依赖版本以 `WeKnora/go.mod`、`WeKnora/frontend/package.json`、`structured-data/pyproject.toml` 和 `commodity-research/requirements.txt` 为准。Docker 与 Linux/WSL 原生运行是两种本地运行方式，配置和数据路径不同，不应交叉覆盖。

本地配置、账号和模型密钥保存在被 Git 忽略的位置。Docker 环境使用 `WeKnora/.env` 和 `.private/model-config.json`；可参考 `WeKnora/.env.example`、根目录 `model-config.example.json`。不要将业务原文件、数据库、密钥或个人账号加入提交。

## Docker 开发

根目录 `start.sh` 使用既有镜像启动服务，不执行构建或覆盖智能体配置。已有环境可执行：

```sh
bash start.sh
```

代码更新后的构建入口：

- Go 应用：`scripts/build_local_app.sh`，复用本地运行镜像与编译缓存；完整镜像构建定义在 `WeKnora/docker/Dockerfile.app`。
- 前端：先在 `WeKnora/frontend/` 安装锁定依赖，再执行 `structured-data/scripts/build_frontend.sh`。
- 表格服务：使用 `structured-data/compose.yml` 中的镜像构建定义；本地配置将 `structured-data/src/` 挂载到容器，Python 修改后需重新启动进程。
- 商品研究：[组件说明](../commodity-research/README.md)与 `commodity-research/compose.yml`。

本地入口由 Compose 配置决定，默认通过 `http://127.0.0.1:18080` 访问工作台。固定的 Compose 项目名用于复用数据卷和网络；不要通过删除数据卷来改名或清理环境。

## Linux / WSL 原生运行

脚本使用本地依赖环境和 `.private/local.env`，以 systemd 用户服务运行。先按脚本要求安装依赖并准备私有配置，再从根目录执行：

```sh
bash scripts/build-local.sh
bash scripts/stop-local.sh
bash scripts/start-local.sh
```

`build-local.sh --clean` 使用空的 Go 编译缓存重新构建。构建成功后才替换产物，旧产物、构建信息与缓存保留在 `.build/`。停止和启动脚本只管理本项目服务。

运行日志位于 `logs/`，表格数据位于 `structured-data/data/`，原生环境的 WeKnora 和商品研究数据位于 `.private/`。这些路径不随源码分发。本地预览默认不启动商品研究自动采集调度。

## 验证

- 表格服务：安装 `structured-data` 的 `test` 依赖后执行 `python -m pytest structured-data/tests`。
- 商品研究：按组件要求安装依赖，以隔离数据库运行 `commodity-research/tests` 和迁移检查。
- 前端：在 `WeKnora/frontend/` 执行类型检查和项目测试。
- Go、发布脚本与镜像：检查范围见 [CI 配置](../.github/workflows/cicd.yml)。

真实原文件和在线模型验收需要单独配置，不能以缺少这些条件的测试结果代替业务验收。开发测试使用隔离的样例与数据目录，不向业务空间上传验收文件。
