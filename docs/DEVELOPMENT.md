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

脚本依赖 `.build/tools/` 中的 Go、Node 和 SQLite 头文件、根目录 `.venv` 及前端依赖；需先准备这些本地依赖。使用 SQLite 和 systemd 用户服务运行时，从根目录执行：

```sh
uv pip install --python .venv/bin/python -r commodity-research/requirements.txt -e ./commodity-research
uv sync --project WeKnora/docreader --locked --no-dev --python python3
bash scripts/build-local.sh
bash scripts/stop-local.sh
bash scripts/start-local.sh
```

构建脚本每次强制检查前端类型并重新编译后端和前端；两者构建成功后替换运行产物，旧产物保存在 `.build/backups/`。当前构建的提交号、时间和校验值记录在 `.build/build-info.json`。仅执行 `start-local.sh` 会复用已有产物，已运行的服务也会继续运行；源码修改后使用上述构建、停止和启动顺序。

若要排除旧编译缓存的影响，先停止项目服务，再执行全量重建：

```sh
bash scripts/stop-local.sh
bash scripts/build-local.sh --clean
bash scripts/start-local.sh
```

`--clean` 使用空的 Go 缓存并强制重新编译全部依赖，前端重新检查类型和生成生产包；成功后保留新的缓存，旧缓存随旧产物一起备份。脚本用文件锁防止多个终端同时构建。停止项目服务可以降低构建时的总内存占用，无需关闭 WSL。

构建通过 systemd 用户 scope 限制资源：内存软阈值 3.5 GiB、硬上限 4 GiB、swap 上限 1 GiB、CPU 配额 2 核；Go 编译并发为 1，Node 堆上限为 3 GiB。达到硬上限时构建任务可能失败，已有运行产物仍保留。

本地环境配置在 `.private/local.env`，账号在 `.private/accounts.json`，WeKnora 数据库在 `.private/weknora/`，结构化数据在 `structured-data/data/`；运行日志在 `logs/`。启动脚本先用独立的 `WeKnora/docreader/.venv` 运行 DocReader，在 `127.0.0.1:50051` 完成 gRPC 健康与鉴权解析器检查后再启动后端；后端使用相同地址及鉴权配置。DocReader 默认本地令牌保存在 `.private/docreader-service-token`，若 `.private/local.env` 设置了 `GRPC_AUTH_TOKEN`，两端会共同使用该配置。缺少解析依赖或 DocReader 检查失败会终止启动，日志在 `logs/docreader.log`。TXT 等简单文本沿用 Go 的内置解析器，DOCX、PDF 等文档通过 DocReader 解析；扫描 PDF 的 OCR 仍需单独配置。

启动脚本同时运行商品研究服务，其空间数据库保存在 `.private/commodity-research/`，内部服务签名密钥保存在 `.private/research-service-token`。研究服务使用 18083 端口，经 18080 的 `/research/` 代理访问；表格服务使用 18082 端口，经 `/structured/` 代理访问。本地预览不启动自动取数调度。修改 Python 后停止并重新启动即可加载当前源码。网页地址与上方相同，知识库入口为 `http://127.0.0.1:18080/platform/knowledge-bases`；进入具体知识库后可直接添加文档并选择文件或文件夹。

本地服务与离线解析回归：

```sh
.venv/bin/python -m unittest discover -s scripts/tests -p 'test_local_services.py' -v
PYTHONPATH=WeKnora WeKnora/docreader/.venv/bin/python -m unittest discover -s WeKnora/docreader/tests -p 'test_runtime_integration.py' -v
```

离线解析回归在随机回环端口运行真实 DocReader，验证 Markdown、含表格的 DOCX 和原生文本 PDF 的普通及流式 gRPC 请求，包含鉴权失败和错误文件后的恢复，不调用模型或外部 OCR。

上传接口返回成功表示原文件已接收，文档仍须完成解析及知识库启用的索引才能显示为完成。若 DocReader 已连接但文件仍失败，先查看文件记录的 `error_message` 和 `logs/backend.log`，区分解析错误与向量接口错误，不要直接修改完成状态或关闭索引掩盖问题。

远程向量化的后台请求遇到 HTTP 429 时，会让同模型在当前进程内的其他后台请求共同等待，并只重试失败的协议请求；同一轮已成功的请求结果保留，不再由检索层重跑整批。最多尝试 6 次，默认退避从 5 秒递增至 60 秒并加随机抖动，遵守响应中的 `Retry-After`。额度耗尽、认证和参数错误直接报告；预算耗尽后仍会失败，由现有文档队列按其规则重试，不能保证平台持续限流时也完成入库。取消任务会中断等待。

后台向量请求默认采用自适应节奏：`EMBEDDING_REQUEST_INTERVAL` 留空或设置为 `0s` 时，正常请求没有额外的固定等待，吞吐由工作池、模型并发上限和供应商响应速度共同决定；发生 429 后，同模型共享冷却并暂时增加至少 1 秒间隔，每连续成功 20 次将自适应间隔减半，直至恢复无额外等待。协调状态按模型 ID、服务地址和模型名称隔离，切换供应商地址或模型不会继承旧服务的冷却。

只有需要人工限制速率时才设置非零的 `EMBEDDING_REQUEST_INTERVAL`，例如诊断时的 `1s`；它是自适应提速的最小间隔，配置变更后须重建 app 容器生效，不作为所有供应商的长期默认值。该间隔独立于 `BATCH_EMBED_SIZE`（每个池子批的输入条数）和 `CONCURRENCY_POOL_SIZE`（工作池大小），不代表模型平台公开配额，不控制其他应用的请求；多副本部署需要按共享配额另行协调速率。前台查询向量化不使用这套后台退避。调整前须核对实际产品入口：Coding Plan 的 `/api/coding/v3` 与普通推理的 `/api/v3` 计费及配额不同，不能为解决 429 直接替换入口。

阿里云多模态向量模型需要选择“阿里云 DashScope”厂商；明确选择“通用”会保留 OpenAI 兼容协议，不能自动替代厂商原生接口。向量维度也须按具体模型配置，例如 `tongyi-embedding-vision-plus-2026-03-06` 支持 64、128、256、512、1024、1152 维，不支持 2048 维，参见[阿里云多模态向量 API 文档](https://help.aliyun.com/zh/model-studio/multimodal-embedding-api-reference)。通过现有模型设置修改厂商和维度会保留模型 ID 及凭据，后续请求即时读取新配置，无需重启。已有成功索引若使用不同模型或维度，须先备份并按正常重解析流程重建，不能直接混用旧向量。重试真实文件前应核对向量模型及摘要、问题生成模型的免费额度；接收、解析文件成功并不代表这些后续模型调用免费。

## 验证

- 表格服务：安装 `structured-data` 的 `test` 依赖后执行 `python -m pytest structured-data/tests`。
- 商品研究：按组件要求安装依赖，以隔离数据库运行 `commodity-research/tests` 和迁移检查。
- 前端：在 `WeKnora/frontend/` 执行类型检查和项目测试。
- Go、发布脚本与镜像：检查范围见 [CI 配置](../.github/workflows/cicd.yml)。

真实原文件和在线模型验收需要单独配置，不能以缺少这些条件的测试结果代替业务验收。开发测试使用隔离的样例与数据目录，不向业务空间上传验收文件。
