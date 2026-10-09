# WeKnora Workbench

基于 WeKnora 的通用结构化表格工作台。这里是开发、运行和 Git 版本管理的统一目录，包含修改后的 WeKnora、Python 数据服务、前端集成和自动化测试。

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
- 确定性查询和计算，返回原文件、工作表、数值与日期单元格。
- 原生 Excel 图表读取及源数据关联；不解析截图式图表。
- 工作台嵌入 WeKnora，复用登录；模型可在页面配置。
- 极值查询绑定真实数值所在行的日期；工具调用采用绑定函数入口。

## 目录

| 路径 | 内容 |
|---|---|
| `WeKnora/` | 上游源码及本项目修改，非 submodule |
| `structured-data/src/structured_data/` | 解析、目录、查询、模型辅助与工作台 |
| `structured-data/tests/` | 单元与集成测试，主要使用程序生成的样本 |
| `structured-data/scripts/` | 构建、注册和本地验收工具 |
| `compose.test.yml` | 本地试点部署覆盖配置 |
| `model-config.example.json` | 不含密钥的模型配置示例 |

上游：Tencent/WeKnora，基线提交 `3e8b0bfc80b845b2d4b2ed683994748741450a97`。上游许可证与版权声明位于 `WeKnora/LICENSE` 等原文件中，已完整保留。初始提交采用源码快照，未导入上游全部 Git 历史；后续修改直接在本仓库维护。

## 本地配置与构建

依赖：Docker、Node.js/npm、Python 3.12+。后端完整源码构建还会下载 Go、Rust 与原生解析依赖，首次构建较慢。

1. 按 `WeKnora/.env.example` 和上游部署说明准备 `WeKnora/.env`。
2. 将 `model-config.example.json` 复制到 `.private/model-config.json`，填写自己的模型配置。账号注册后，可在 `.private/accounts.json` 保存注册脚本所需的本地账号列表（email/password）。这些文件均被忽略。
3. 构建修改后的后端：

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

## 测试

```sh
cd structured-data
python3 -m venv .venv
.venv/bin/pip install -e '.[test]'
.venv/bin/python -m pytest tests
```

后端调用回归：在 `WeKnora` 中执行 `go test ./internal/agent/tools ./internal/agent -run TestMCP -count=1`。部分测试依赖原始验收文件或运行中的服务，需要单独配置。

本仓库不包含公司工作簿、数据库快照、模型密钥、账号、聊天记录、运行报告与截图。部分历史文档中的本地验收链接不会在本仓库提供；真实数据验收 JSON 也不提交，缺失时对应测试跳过。历史测试证据保存在本机 `.local-backups/legacy-evaluation-20261009.tar.gz`。

## 仓库体积与备份

当前代码树不包含上游独立官网/文档站；保留应用源码、前端运行资源、API 文档、许可证和本项目文档。上游文档入口位于 `WeKnora/website-docs/README.md`。

`.local-backups/` 保存旧目录独有资料和上游文档站的压缩备份，已被 Git 忽略。本地数据库、虚拟环境和备份不代表上传到 GitHub 的仓库大小。

初始提交包含上游文档站；普通删除提交会减小当前代码树，但 Git 历史仍保留这些文件。此整理不重写已发布历史，也不执行强制推送。
