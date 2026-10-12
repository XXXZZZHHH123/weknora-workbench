# 商品研究工作台

Commodity Research Platform 已作为 Python 研究服务合入 WeKnora Workbench，来源与提交见 [SOURCE.json](SOURCE.json)。原仓库保留，后续功能在此目录维护。

首版完成统一仓库、登录、研究入口、空间共享事实与个人研究归属，以及聊天只读查询研究结果。跨服务 Excel 指标映射属于下一阶段。CRP 原有人工录入、Excel/终端导入、指标、产业图、判断、简报、策略和导出继续保留。

## 使用

主仓运行 `bash start.sh`，左侧打开“商品研究”（`/platform/commodity-research`）。研究页面与 API 位于同源 `/research/`。聊天选择“商品研究助手”，或在自定义智能体勾选“商品研究”工具。

事实、指标口径、产业图、导出模板在空间内共享；判断、版本、信号、待办、简报、思路和策略按账号隔离。作者来自服务端身份。切换账号或空间后重新核验，旧标签页提交与当前会话不符时拒绝写入。

本机旧库复制迁入空间 10000，默认研究员 `me` 绑定 `eval_a_28c1c2679d@example.com`，原库保留。服务器数据尚未迁移，发布仍由主仓 `deploy/update.sh` 手动触发。

## 架构

- `web/`：FastAPI、Jinja2 和普通 JavaScript，统一处理路径前缀。
- `integration/`：WeKnora 会话、空间身份、服务器签名只读 API。
- `models/`、`alembic/`：SQLAlchemy 模型与迁移。
- `ingest/`：官方源、人工录入、Excel/终端文件、离线原件。
- `compute/`：派生值、时点与口径守卫、信号与待办。
- `judgments/`、`brief/`、`strategy/`：个人判断、简报、策略与成交记录。
- `ownership.py`：个人查询与文件访问边界。
- `llm/`：生成 provider、审计与证据校验。

每个空间使用 `data/workspaces/<tenant_id>/tin.db`，首次只补公共指标，不复制个人判断。迁移使用进程及文件锁，迁移前备份。默认单 Web worker，复杂并发另行评估 PostgreSQL。

聊天在原有 WeKnora Agent 循环中调用 `commodity_research`。账号、空间取自已核验 caller，两端私有密钥签名；模型不能传入账号、空间或服务地址。七项读取为 summary、indicators、series、judgment、signals、brief、strategies。序列返回真实观测 ID、数值、口径、时点、修订和来源；不存在的简报明确返回缺口。

历史查询的事实、信号和简报按日期读取；判断仍是当前账号最新生效版或最新草稿，返回明确标注，不能当作历史回测。集成日报按账号从数据库重组；旧冻结 JSON 保存于迁移证据目录。

## 数据迁移

先预览再应用。脚本通过 WeKnora 核验邮箱与空间，SQLite backup 一致复制原库与 WAL，核对每张旧表数量、观测全文和外键后发布。旧 exports/raw 保存在目标空间 legacy/。相同迁移可重跑，不匹配目标拒绝覆盖。

```sh
cd /Users/mac/Code/weknora-workbench/commodity-research
PYTHONPATH=src python scripts/migrate_legacy.py \
  --source /Users/mac/Code/Commodity-Research-Platform/data/tin.db
PYTHONPATH=src python scripts/migrate_legacy.py \
  --source /Users/mac/Code/Commodity-Research-Platform/data/tin.db --apply
```

账号文件默认主仓 `.private/accounts.json`，不打印密码或 token。`--tenant` 可指定有权限的目标空间，不把旧个人判断当成空间种子。

## 聊天注册

脚本只创建或更新带项目标记的“商品研究助手”，使用已有聊天模型，不改其他智能体和凭据。默认预览，多个模型时明确 `--model-id`。

```sh
PYTHONPATH=src python scripts/register_weknora.py --model-id '<已有模型ID>'
PYTHONPATH=src python scripts/register_weknora.py --model-id '<已有模型ID>' --apply
```

## 取数与生成

集成模式拒绝无身份上下文的旧 `python -m tin.jobs daily`。显式核验账号后运行原任务：

```sh
PYTHONPATH=src python scripts/run_workspace.py fetch \
  --email eval_a_28c1c2679d@example.com --tenant 10000
PYTHONPATH=src python scripts/run_workspace.py daily \
  --email eval_a_28c1c2679d@example.com --tenant 10000
```

首版不自动安装生产定时任务。账号需空间写入权限。聊天使用 WeKnora 所选模型；CRP 简报/策略生成暂沿用显式 `TIN_LLM_*` 配置，统一生成模型入口留待后续。

## 开发与验证

Python 3.12+，在本目录创建虚拟环境：

```sh
pip install -r requirements.txt
PYTHONPATH=src TIN_LLM_BASE_URL='' TIN_LLM_API_KEY='' \
  TIN_DATABASE_URL=sqlite:// TIN_AUTO_MIGRATE=false pytest -q
```

测试隔离本机模型配置。SPEC.md 为原需求设计，预留表不等于完整交付；思路蒸馏、终端自动导出和跨服务 Excel 映射不在本次首版。

镜像无需私有 PRD。数据库、原件、密钥、暂存、导出与验收记录不提交 Git。备份恢复见主仓 [部署说明](../deploy/README.md)。
