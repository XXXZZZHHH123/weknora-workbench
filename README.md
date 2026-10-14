# WeKnora Workbench

基于 WeKnora 的通用结构化表格工作台。这里是开发、运行和 Git 版本管理的统一目录，包含修改后的 WeKnora、Python 数据服务、前端集成和自动化测试。

现已合入 [商品研究工作台](commodity-research/README.md)：统一 WeKnora 登录与空间，指标事实在空间内共享，判断、简报、研究思路和策略按账号保存。左侧“商品研究”打开研究页面；“商品研究助手”通过只读工具查询真实研究结果。首版完成仓库、账号、入口和聊天读取；Excel 与研究指标互通留待下一阶段。

## 本机日常使用

当前项目目录：`/Users/mac/Code/weknora-workbench`。在此修改源码、运行测试和提交 Git；旧 `weknora-evaluation` 目录已合并清理；独有资料保存在本目录 `.local-backups/` 的压缩备份中，不再保留第二个工程目录。

已构建镜像后，启动现有服务：

```sh
cd /Users/mac/Code/weknora-workbench
bash start.sh
```

启动脚本复用已有镜像及数据，不会自动重新注册智能体或覆盖其配置。修改 Go 后端或前端后，需先按下方步骤重新构建镜像，再执行启动脚本；结构化服务的 `src` 挂载自本目录，修改 Python 后执行 `docker compose -p weknora-structured -f structured-data/compose.yml restart structured-data`。

本地模型密钥和账号位于 `.private/`，WeKnora 环境配置位于 `WeKnora/.env`，结构化数据库和原文件位于 `structured-data/data/`；这些均被 Git 忽略。WeKnora 账号、知识库和聊天记录继续保存在原有 Docker 数据卷中。

Docker 项目名 `weknora-eval`、`weknora-structured` 保留以复用现有数据卷及网络，不代表服务仍从旧目录运行。不要为改名删除数据卷。迁移和回滚说明见 [目录迁移说明](MIGRATION.md)。

## 当前能力

- Excel 工作表扫描、复杂区块映射、解析预览与人工确认。
- 基于真实字段目录的本地检索与模型语义理解，歧义时通过页面确认。
- 字段目录查询按真实指标名称分类、计数并返回表头证据；多个合约列不增加指标种类，无需具体日期。
- 页面确认将选择和补充说明原样交给已授权的同一服务执行一次；只有真实歧义显示确认入口，系统错误单独显示。
- 确定性查询和计算，返回原文件、工作表、数值与日期单元格。
- 原生 Excel 图表读取及源数据关联；不解析截图式图表。
- 工作台嵌入 WeKnora，复用登录；模型可在页面配置。
- 极值查询绑定真实数值所在行的日期；复合统计可同时返回最高、最低及各自日期，并由程序计算差值。已确认口径用于连续追问。
- 结构化答案与确认提示在本轮完成后显示，调用重试过程保留在详情中。

## 目录

| 路径 | 内容 |
|---|---|
| `WeKnora/` | 上游源码及本项目修改，非 submodule |
| `structured-data/src/structured_data/` | 解析、目录、查询、模型辅助与工作台 |
| `structured-data/tests/` | 单元与集成测试，主要使用程序生成的样本 |
| `commodity-research/` | 商品指标采集、判断监测、简报、策略、空间与账号隔离 |
| `structured-data/scripts/` | 构建、注册和本地验收工具 |
| `compose.test.yml` | 本地试点部署覆盖配置 |
| `model-config.example.json` | 不含密钥的模型配置示例 |

上游：[Tencent/WeKnora](https://github.com/Tencent/WeKnora)。初始源码基线为正式版 `v0.8.2` 的提交 `3e8b0bfc80b845b2d4b2ed683994748741450a97`，当前合入的上游版本记录在 `.upstream/weknora.json`。许可证与版权声明位于 `WeKnora/LICENSE` 等原文件中，已完整保留。初始提交采用源码快照，未导入上游全部 Git 历史；后续修改直接在本仓库维护。

每天北京时间 10:20 的 GitHub 工作流检查 WeKnora 正式发布，有更新时通过三方合并生成更新 PR；GitHub 可能延迟定时运行，也可在 Actions 手动运行。更新不会自动合并，需审查测试与业务兼容性后由你确认；合并 `main` 后才进入现有 CI/CD。冲突 PR 保持草稿并阻止检查通过。操作和冲突处理见 [上游更新说明](deploy/UPSTREAM.md)。

## 本地配置与构建

依赖：Docker、Node.js/npm、Python 3.12+。后端完整源码构建还会下载 Go、Rust 与原生解析依赖，首次构建较慢。

1. 按 `WeKnora/.env.example` 和上游部署说明准备 `WeKnora/.env`。
2. 将 `model-config.example.json` 复制到 `.private/model-config.json`，填写自己的模型配置。账号注册后，可在 `.private/accounts.json` 保存注册脚本所需的本地账号列表（email/password）。这些文件均被忽略。
3. 已有本地运行镜像时，日常调试可复用运行环境和 Go 缓存，只重新编译应用（使用现有外部 docreader；内置 anydoc 不在此快速构建中）：

   ```sh
   bash scripts/build_local_app.sh
   ```

   首次启动或需要完整运行环境时，构建完整后端：

   ```sh
   docker build -f WeKnora/docker/Dockerfile.app -t weknora-eval-app:mcp-direct WeKnora
   ```

4. 在 `WeKnora/frontend` 执行 `npm ci`，再从根目录执行 `bash structured-data/scripts/build_frontend.sh`。
5. 启动服务：

   ```sh
   docker compose -p weknora-eval --project-directory ./WeKnora -f WeKnora/docker-compose.yml -f compose.test.yml up -d --no-build
   docker compose -p weknora-structured -f structured-data/compose.yml up -d --build
   ```

6. 在 WeKnora 注册并配置聊天模型，然后按 `structured-data/docs/WeKnora接入.md` 执行注册脚本。`STRUCTURED_TENANT_ID` 等试点值须按实际部署调整。

网页：`http://127.0.0.1:18080`；集成工作台：`http://127.0.0.1:18080/platform/structured-data`。

### Linux / WSL 原生本地运行

本工作区已安装 `.build/tools/` 中的 Go、Node 和 SQLite 头文件、根目录 `.venv` 及前端依赖。使用 SQLite 和 systemd 用户服务运行时，从根目录执行：

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

阿里云多模态向量模型需要选择“阿里云 DashScope”厂商；明确选择“通用”会保留 OpenAI 兼容协议，不能自动替代厂商原生接口。向量维度也须按具体模型配置，例如 `tongyi-embedding-vision-plus-2026-03-06` 支持 64、128、256、512、1024、1152 维，不支持 2048 维，参见[阿里云多模态向量 API 文档](https://help.aliyun.com/zh/model-studio/multimodal-embedding-api-reference)。通过现有模型设置修改厂商和维度会保留模型 ID 及凭据，后续请求即时读取新配置，无需重启。已有成功索引若使用不同模型或维度，须先备份并按正常重解析流程重建，不能直接混用旧向量。重试真实文件前应核对向量模型及摘要、问题生成模型的免费额度；接收、解析文件成功并不代表这些后续模型调用免费。

## CI/CD 与服务器部署

GitHub Actions 在 PR 和 main 更新时执行检查；main 检查通过后构建 linux/amd64 镜像并发布部署包。Go 依赖保存在可复用的 Docker 层，编译缓存单独跨构建保存；依赖清单不变时无需每次重新下载整套 Go 模块。

在内网服务器手动运行 `bash ~/weknora-workbench/bin/update.sh` 拉取最新已发布版本并部署；不设置定时获取任务。适配 CentOS 7 / Docker 24，无需服务器运行 GitHub Actions Runner。首次配置、只读凭据、健康检查、备份和恢复见 [服务器部署说明](deploy/README.md)。

## 测试

```sh
cd structured-data
python3 -m venv .venv
.venv/bin/pip install -e '.[test]'
.venv/bin/python -m pytest tests
```

后端调用回归：在 `WeKnora` 中执行 `go test ./internal/agent/tools ./internal/agent -run TestMCP -count=1`。部分测试依赖原始验收文件或运行中的服务，需要单独配置。

结构化导入与查询浏览器回归从仓库根目录执行 `node structured-data/tests/workspace-ui.cjs`，需要 Node.js 22+ 和本机 Chromium。默认从 Playwright 浏览器缓存查找 Chromium，也可用 `SHEET_REVIEW_BROWSER` 指定已有可执行文件；无需新增 npm 依赖。测试运行真实页面脚本与隔离 HTTP 夹具，验证文件选择、查询及原单元格证据、旧结果清理、知识库隔离和窄屏交互，不调用云模型。默认临时文件会清理；设置 `STRUCTURED_UI_ARTIFACTS` 可保留结果和截图。

本仓库不包含公司工作簿、数据库快照、模型密钥、账号、聊天记录、运行报告与截图。部分历史文档中的本地验收链接不会在本仓库提供；真实数据验收 JSON 也不提交，缺失时对应测试跳过。历史测试证据保存在本机 `.local-backups/legacy-evaluation-20261009.tar.gz`。

## 仓库体积与备份

当前代码树不包含上游独立官网/文档站；保留应用源码、前端运行资源、API 文档、许可证和本项目文档。上游文档入口位于 `WeKnora/website-docs/README.md`。

`.local-backups/` 保存旧目录独有资料和上游文档站的压缩备份，已被 Git 忽略。本地数据库、虚拟环境和备份不代表上传到 GitHub 的仓库大小。

初始提交包含上游文档站；普通删除提交会减小当前代码树，但 Git 历史仍保留这些文件。此整理不重写已发布历史，也不执行强制推送。
