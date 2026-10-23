# 独立 MinerU Standard 服务

该目录与现有应用同仓库，单独构建镜像、单独 Compose project；不需要为了一个解析 worker 新建 GitHub 仓库。独立 workflow 只在本目录或自身文件变更时触发。镜像构建/推送不等于服务器自动部署，当前没有远程部署步骤。

默认组合固定为 MinerU **4.0.12**、CPU ONNX 小模型、官方 **MinerU2.5-Pro-2605-1.2B GGUF + llama.cpp**。`mineru-llama-cpp==0.1.4`，不安装 Torch/vLLM，不向容器映射 GPU。已存在的 Basic ONNX 模型缓存可原位复用，Standard 只需再补官方 GGUF 主模型与 mmproj。既有 Basic 成功不代表 Standard 已通过验收。

## 私有文件与缓存

部署配置和 API key 放在仓库外。创建供 UID/GID `10001:10001` 读取的私有 JSON；容器只读挂载，不能放进构建上下文、Git、build args 或命令行。默认本地模式只需要：

```json
{"api_key":"替换为至少32字符的独立随机服务密钥","vlm_mode":"local"}
```

此 key 是 WeKnora 到解析服务的 bearer key，不是模型网关 key。服务不接入/复制 WeKnora 模型账户。JSON 中本地模式若夹带远程地址/key/model 会直接拒绝启动。

部署环境只包含路径和资源设置：

```bash
export MINERU_IMAGE='ghcr.io/OWNER/REPOSITORY-mineru:4.0.12-COMMIT'
export MINERU_SETTINGS_FILE='/absolute/private/mineru.json'
export MINERU_DATA_DIR='/absolute/private/mineru-data'
export MINERU_MODEL_CACHE_DIR='/absolute/existing/mineru-home/models'
export MINERU_WORKBENCH_NETWORK='weknora-workbench-dev_default'
```

创建数据目录并授予 UID 10001 读写权；复用既有用户所有的缓存时，可设置 `MINERU_UID` / `MINERU_GID` 为该用户的非 root ID，无须放宽目录权限。模型缓存同样需要读写，因为 MinerU 会创建模型锁文件。密钥文件应仅允许运行用户读取（例如 `0600`），不要使用 `chmod 777`。不要把整个宿主私有目录或 Docker socket 挂入容器。模型根应直接包含以下目录，不能多套一层 `models/`：

```text
models/
  MinerU-4_models_onnx/                    # 既有 Basic 缓存
  MinerU2.5-Pro-2605-1.2B-GGUF/
    MinerU2.5-Pro-2605-1.2B-Q8_0.gguf
    mmproj-MinerU2.5-Pro-2605-1.2B-Q8_0.gguf
```

使用已安装的同版本 MinerU 环境准备/核验模型，保留已有缓存。以下是实际 4.0.12 CLI 参数；下载是独立准备步骤，运行服务固定 `model.source=local`，缺模型时应失败，不在业务请求中隐式下载：

```bash
MINERU_MODEL_BASE_DIR="$MINERU_MODEL_CACHE_DIR" \
  mineru-kit models download --tier standard --small-backend onnx --vlm-engine llama-cpp --source huggingface
MINERU_MODEL_BASE_DIR="$MINERU_MODEL_CACHE_DIR" \
  mineru-kit models verify --tier standard --small-backend onnx --vlm-engine llama-cpp
```

根据实际网络可明确改用 `--source modelscope`。模型来源、实际 revision/hash、下载耗时及完整性核验应由部署记录保存；镜像固定 Python 包版本不等于固定模型仓库未来 revision。

## 检查与启动

以下命令只有显式执行才会启动独立服务；不会修改已有应用 Compose 文件或重启应用。先确认外部开发网络真实存在：

```bash
docker compose -f services/mineru/compose.yml config --quiet
docker compose -f services/mineru/compose.yml build mineru
docker compose -f services/mineru/compose.yml run --rm --no-deps mineru --check
docker compose -f services/mineru/compose.yml up -d --no-build --wait mineru
docker compose -f services/mineru/compose.yml exec mineru python /opt/mineru/entrypoint.py --healthcheck
```

从仓库根执行。CI 镜像已可用时先 `pull`，替代 `build`；发布时优先用审查过的不可变镜像 digest。Compose project 默认 `weknora-mineru`，只有新 worker 加入现有开发外部网络；还建立一个内部私有网络。现有开发网络可能允许出网，这不是目标域名白名单。宿主只绑定 `127.0.0.1:18733`，可通过 `MINERU_HOST_PORT` 改本机端口；跨机访问应经现有受控隧道。未来结构化服务可使用同网 `http://mineru:8000`，不需要使用宿主映射端口。

健康检查要求 `/v1/health` 返回 `ok`、精确版本 `4.0.12`，且 `/v1/tiers` 仅有 `basic`、`standard`。启动预加载模型，预加载失败的 503 会让健康检查失败；不会用 Basic 假装 Standard ready。健康通过仍不是识别正确性证明：至少用原先的两页重复表头、无表头续表、同形不同单位独立表和公式/负数/空值样例，走真实上传→任务→ZIP→规范化→审校→只读计算，核对原图、来源页码与数值。

## 资源与边界

默认限额 `2 CPU / 6 GiB`，任务并发 1、VLM 客户端额度 2，数据保留 86400 秒。可设置 `MINERU_CPUS`、`MINERU_MEMORY`、`MINERU_PARSE_CONCURRENCY`、`MINERU_VLM_CONCURRENCY`、`MINERU_RETENTION_SECONDS`。这是受限环境的起始试运行配置；官方安装说明给 ONNX+llama Standard 的内存建议是 **8 GB**，6 GiB 需要实测峰值/是否 OOM，不能据健康接口判断容量足够。

4.0.12 的 `VlmConfig` 不公开 llama.cpp 的 `n_threads/n_parallel` 设置，本封装不修改官方引擎。Docker CPU 配额限制实际使用量；上游本地 Engine 仍使用默认线程自动探测和 4 slots，VLM 客户端额度 2 不等于 Engine slots 被改为 2。容器无 GPU 设备时使用 CPU 路径；如果需要 GPU，必须另行选择部署配置和重新验收。

根文件系统只读、非 root、删除 capabilities、禁止提权。`/tmp` 保留 512 MiB tmpfs，但 PDF 渲染通过 `TMPDIR=/var/lib/mineru/data/tmp` 使用解析数据卷的磁盘空间；启动前验证可写，正常完成或失败由上游临时目录上下文清理。异常强杀后须按实际任务状态清理残留，不能删除正在处理的目录。显式设置 `MINERU_PROCESSING_WINDOW_SIZE=8`（支持 1–32），限制同时渲染的页数；它不是文档总页数限制。Linux 渲染会写入原始位图，64 页可能超过 512 MiB，不能用增大共享内存替代此配置。磁盘容量仍需监控。

API bearer key 放在进程内参数，不经 CLI flags。Flash/Advanced 与独立 LLM 自动标题分层/跨页单元格合并关闭，跨页逻辑关系仍由项目自己的显式审校计划控制；窗口边界不应作为表格逻辑边界。公式保留不等于公式可执行。

扫描长文档还需要让调用方的等待预算相互匹配。WeKnora 的 `WEKNORA_MINERU_TIMEOUT` 必须小于外层 `WEKNORA_DOCREADER_CALL_TIMEOUT`，两者需给 `knowledge_base.document_process_timeout` 留出分块和入库时间。结构化入口另用 `PDF_TABLE_PARSER_TIMEOUT_SECONDS` 与轮询预算，详见 `structured-data/PDF_PARSER_BRIDGE.md`；不能只改一个超时值，也不能将放宽预算当成性能优化。空间不足现在分别返回 `DOCREADER_STORAGE_EXHAUSTED` / `PARSER_STORAGE_EXHAUSTED`，不再提示用户检查 PDF 格式。

结构化审校当前支持纵向续行；同一组实体的不同指标分列在相邻页的横向续表应保持片段、提示待审核，不能改名后上下追加。各页抽取成功、跨页逻辑表合并成功和数值核对通过是三个独立验收条件。

## 可选专用远程 VLM

只有经过真实 MinerU 文档协议验收的专用视觉服务才能使用此模式，普通“支持图片”的聊天模型不能据此认定适配。配置必须显式改为：

```json
{"api_key":"独立解析服务密钥至少32字符","vlm_mode":"remote","vlm_server_url":"https://dedicated-mineru.example/v1","vlm_api_key":"模型服务密钥","vlm_model":"实际专用模型ID"}
```

非空 `server_url` 按官方客户端走远程协议，`engine=auto`；不会静默切回本地或换模型。URL 不允许用户信息、query 或 fragment。远程服务地址与凭据只从只读挂载的私有配置进入。

## CI/CD

`.github/workflows/mineru-image.yml` 使用与主仓库一致的固定 action SHA；PR 只构建/验证，main 才把测试过的独立镜像推送至 `ghcr.io/<owner>/<repo>-mineru:4.0.12-<commit>`。CI 验证 Compose、配置结构、CPU 依赖和无 Torch 安装；不下载模型、不读取业务 PDF、不调用模型 API。镜像保留包版本固定，构建链中的其他传递依赖仍由上游范围解析；严密复现时应进一步锁定整个依赖集合及基础镜像 digest。

拆成新仓库只在有独立团队、发布权限或复用生命周期需求时才值得承担跨仓版本同步。当前独立目录、独立镜像、独立 workflow 已能隔离构建发布；本实现不创建远程仓库，也不自动部署生产。

## 2026-10-22 开发服务器实际验收

本地官方模型在上述 2 CPU / 6 GiB 配额下启动并完成鉴权 HTTP 上传、Standard 解析、ZIP 下载和结构化桥接。5 份合成样例分别覆盖原生数字表/公式、纯图片数字表/公式、两页重复表头、无表头续页、相邻不同单位的独立表。耗时约 20 / 91 / 10 / 91 / 10 秒，包含轮询等待；这是小样例实测，不代表业务吞吐量。观测内存峰值约 2.53 GiB，未触发内存限额。独立 worker 重启后再次真实解析成功。

数字、空值、负数和分页来源核对通过；重复表头样例按显式测试计划发布到隔离结构化库，四行的只读 SQL 汇总与预期一致。原生样例的分式多识别了一个竖线/数组符号，无表头续页多出一行空白，不能宣称公式或所有单元格完全准确。原始产物完整保存，公式不自动执行；异常需在审校中确认。测试中还修复了桥接层上传 PUT 漏鉴权和字符串公式未归一化的问题，并将默认轮询预算调整为覆盖 600 秒总期限，避免约 80 秒提前结束。

这次只部署独立 worker。既有应用版本与生产服务没有切换，WeKnora 新 PDF 审核界面及问答接入尚未启用；上述测试不是现网业务 PDF 或用户界面的端到端验收。GitHub workflow 尚须提交到远端并触发后才有 CI 发布结果。
