# WeKnora 接入说明

## 接入方式与边界

结构化数据服务独立运行，WeKnora 通过 MCP 查询。大型 Excel 由本服务导入，不必再次上传到 WeKnora 知识库，不触发整表 Embedding。

注册脚本不修改 WeKnora 源码、Compose、系统设置，不启动/停止服务，不删除知识库，也不调用聊天模型。它只在 `--apply` 时写入本项目拥有的 MCP 和智能体配置。服务连通不代表真实问答准确，接入后仍需执行验收问题。

下述部署流程持续适用；早期 v0.8.2 的镜像信息属于历史记录。2026-10-11 本地调试采用自主分析链路，详细验收见 [当前报告](AGENT_HARNESS_EVALUATION_2026-10-11.md)。

## 一、先启动并确认数据服务

服务的启动与导入命令见项目 README。默认主机端口为 `18082`，MCP 路径为 `/mcp`。服务 token 保存于 `data/service-token`，权限必须为 `0600`；脚本不生成、显示、保存副本或自动修改该 token。

预期有11项工具（管理和数据写入不通过 MCP 暴露）：

| 工具 | 用途 |
|---|---|
| `list_datasets` | 查看版本和数据范围 |
| `list_series(dataset_id, search, limit)` | 查找指标、维度、单位、时间范围 |
| `query_series(dataset_id, series_id, start_date, end_date, operation, limit)` | 取数或计算 rows/latest/mean/sum/min/max/count |
| `compare_series(dataset_id, left_series_id, right_series_id, date)` | 比较两个指标并返回差值、来源 |
| `list_charts` | 核查图表来源 |
| `convert_price` | 明确单位和汇率后的价格换算 |
| `resolve_fields` | 可选的语义字段绑定及常见查询 |
| `confirm_fields` | 执行真实用户提交的待确认选择 |
| `execute_field_plan` | 执行已验证的字段方案 |
| `get_result_evidence` | 读取持久化的查询/分析事实 |
| `analyze_table` | 对选定输入执行只读 DuckDB SQL，支持分组、连接、窗口与多阶段计算 |

脚本要求工具目录与这11个名称完全一致，避免意外将管理或数据写入工具暴露给智能体。`list_series`、`query_series`、`analyze_table` 的 dataset_id 可省略，仅在只有一个当前来源文件时自动选取该文件最新版本；多文件时需检索真实 ID，不能任选第一份。

## 二、选择 WeKnora 能访问的地址

| 服务部署位置 | 提供给 WeKnora 的地址 | 需精确放行的主机名 |
|---|---|---|
| 与 WeKnora 在同一个 Docker 网络，服务名 cotton-data | `http://cotton-data:18082/mcp` | `cotton-data` |
| macOS 宿主机 | `http://host.docker.internal:18082/mcp` | `host.docker.internal` |

不能在容器内用 `127.0.0.1:18082` 访问宿主机；该地址指向 WeKnora 容器自身。宿主机服务是否仅绑定回环、Docker Desktop 是否能转发到它，应实测连通性；不要为了接入默认开放到整个局域网。优先使用同网络 sidecar。

WeKnora 会对私网地址进行 SSRF 校验，新增主机必须明确加入白名单，不能关闭整体校验或放行全部私网。

### 配置白名单的两种方法

**系统管理员热更新：**先 GET `/api/v1/system/admin/settings/ssrf.whitelist`，保留现有值并追加所需主机，再 PUT 同一接口，格式为 `{"value":["保留已有主机","cotton-data"]}`。修改立即生效，不需要重启，且仍会合并部署维护的 `SSRF_WHITELIST_EXTRA`。

**部署配置：**保留现有 `SSRF_WHITELIST_EXTRA` 并追加精确主机名，再仅重建 app 容器使环境生效。操作前检查导入任务；运行中的解析/Embedding 可能被中断或重试。此注册脚本不会执行该操作。

2026-09-28 的只读核对中，第一测试账号不是系统管理员，系统设置接口返回 403，但可读取 MCP、智能体、模型列表。若没有管理员账号，需要由部署配置完成白名单。

## 三、先预览，再注册

在 `structured-data` 目录执行：

```bash
python3 scripts/register_weknora.py
```

默认使用：

- WeKnora API：`http://127.0.0.1:18081`
- MCP：`http://cotton-data:18082/mcp`
- 账号：上层 `.private/accounts.json` 的第一组
- 服务凭据：`data/service-token`
- 注册结果：`results/integration.json`

预览会登录并读取已有记录，但不会创建/修改 MCP 或智能体，也不会写注册结果。输出不包含密码、访问令牌或服务 token，记录 ID 已脱敏。默认优先选择活跃 `qwen-max` 聊天模型；同名模型以稳定 ID 排序，不自动测试付费模型调用。可用 `--model-id` 指定已验证模型。

确认网络与服务准备好后应用：

```bash
python3 scripts/register_weknora.py --apply
```

宿主机运行服务时：

```bash
python3 scripts/register_weknora.py --url http://host.docker.internal:18082/mcp --apply
```

注册顺序：

1. 登录，确认租户，选择聊天模型。
2. 仅创建/更新本项目标记的 MCP。
3. 经 `/mcp-services/{id}/credentials` 写入 Bearer token；不放入普通 headers 或结果文件。
4. 调用连接测试，确认 WeKnora 容器本身能连通服务。
5. 显式刷新 `/metadata/refresh`，再读取保存的工具目录核对11项能力。
6. 仅在上述成功后创建/更新“表格数据分析（结构化）”智能体。
7. 保存不含凭据的 `results/integration.json`，包含服务/智能体 ID、模型 ID、状态和工具名。

智能体只选择此 MCP，不关联知识库或技能；内置工具仅保留 `thinking`。这套小工具集采用 `mcp_tool_exposure_mode=direct`，省去逐项发现定义的额外模型回合，但执行仍经过现有授权和参数校验；其他智能体默认保持延迟发现。关闭自动问题推荐。联网仅在用户明确允许或本地工具明确给出可用的公开数据回退时使用；本轮验收请求禁用联网。

仅更新镜像不会更新数据库中已经保存的智能体提示词、模型选择或 MCP 目录。升级后对原有所有权标记运行注册脚本，使用生产环境自己的 API、账号、token 和 state 文件；不要复制本地账号或 ID。先默认预览，确认它将更新原有记录，再 `--apply`。`--model-id` 指定该环境已配置的模型 ID；`--thinking` 开启智能体推理，省略则保留既有开关。可用 `--thinking-budget 2048` 设置每轮推理预算，0使用模型默认预算；仅支持该参数的供应商会发送。模型本身也须在设置中选择与真实端点一致的供应商，并配置正确的 thinking_control（本地 deepseek-v4.1-flash 使用 enable_thinking），注册脚本不会擅自改模型参数或凭据。

## 四、幂等与失败处理

- `integration.json` 记录随机所有权标记。更新必须同时匹配当前 API、租户以及服务器记录中的该标记。
- 同名但没有本项目标记的服务或智能体不会被覆盖，脚本会停止。
- 连接失败时保留已创建的自有 MCP 记录，以便修复网络后再次运行；不会继续创建智能体。
- 不要删除 `integration.json` 后盲目重跑。丢失记录时脚本会拒绝接管同名服务，应先恢复该文件。
- 工具定义变化后重新执行 `--apply` 会刷新保存的目录；随后在网页新建对话，避免旧会话持有过期工具引用。
- 注册成功仅设置 `llm_quality_verified=false`；必须另测模型是否正确选指标、日期和差值方向。
- 脚本不自动回滚或删除失败产生的记录，以免误删用户数据。必要时依据结果文件中的自有 ID 手工清理。

## 五、网页验收

进入 WeKnora 智能体页面，选择“棉花数据分析（结构化）”新建对话。先问“当前有哪些数据集、各自覆盖哪些日期”，再测试明确的单日数值和区间统计。检查工具调用记录，以及回答中的文件、工作表、单元格来源。

不要仅凭模型说“已查到”判断接入成功。至少核对一个点值、一个差值、一个超出日期范围的拒答。完整测试记录采用“问题—预期答案—实际结果—差距”。

## 六、源码依据

- MCP 路由：`WeKnora/internal/router/routes_infra.go`
- MCP 配置：`WeKnora/internal/types/mcp.go`
- 凭据专用接口：`WeKnora/internal/handler/mcp_credentials.go`
- 工具目录刷新：`WeKnora/internal/handler/mcp_metadata.go`
- 智能体配置：`WeKnora/internal/types/custom_agent.go`
- MCP 独立注册：`WeKnora/internal/application/service/agent_service.go`
- 白名单热更新：`WeKnora/internal/application/service/system_setting.go`
