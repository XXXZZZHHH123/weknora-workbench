# 对话中的文档生成

## 官方机制和本次修复

[官方技能说明](https://github.com/Tencent/WeKnora/blob/main/website-docs/03-features/22-skills-sandbox.md)要求创建空间沙箱配置、安装技能并绑定智能体。模型读取技能说明，通过沙箱执行脚本；`/workspace/output` 中的文件由官方 ArtifactCollector 保存，沿用会话及消息权限提供下载。

官方没有保证未配置沙箱的普通问答可以直接生成 Word/PDF。我们的工作台把快速问答也接入了工具循环，但旧适配代码清空了它的技能和沙箱选择，因此仅完成官方配置仍无法从普通入口使用。本次保留管理员的显式配置，在快速问答编辑器显示技能分区，不自动开放其他空间、自定义智能体或额外 MCP 服务。

`skills/document-export` 是通用排版技能：模型依据完整对话和真实证据整理 JSON，脚本不接收问题、不检索业务数据、不决定来源或数值。Word 和 PDF 使用同一份排版，通过 LibreOffice 转换；重新打开文件并返回大小、SHA256 和页数后才交付。来源标题、工作表、页码、单元格和限制由模型保留在内容中。实际生成样例不随业务文件上传。

## 本地配置

Docker 运行正常后，在仓库根执行：

```sh
docker build -f deploy/Dockerfile.documents -t weknora-document-sandbox:local .
mkdir -p .private
touch .private/document-runtime.enabled
bash scripts/build_local_app.sh
bash structured-data/scripts/build_frontend.sh
bash start.sh
```

`.private/document-runtime.enabled` 是管理员显式启用本地 Docker 沙箱的标记。启动脚本加载 `compose.documents.yml`，通过独立 Unix socket 转发服务向可信 app 提供 Docker API，不挂给会话或技能容器。转发服务只在私有数据卷创建组权限为 0660 的 socket，无 TCP 监听，不修改宿主机 socket 权限；这也兼容 Docker Desktop 的 root 组挂载。这个权限等同控制 Docker daemon；沿用官方启用开关和空间策略，不改变生产 Compose 的默认关闭状态。移除标记后下次启动不再加载此覆盖配置。

随后在页面按官方流程安装技能，或使用管理员登录令牌文件配置：

```sh
python3 scripts/configure-document-generation.py \
  --url http://127.0.0.1:18081 \
  --token-file /安全路径/admin-token \
  --image weknora-document-sandbox:local
```

令牌文件只含登录令牌，不要放进命令行或提交仓库。脚本使用官方 API，服务端检查当前空间 Admin/Owner 权限。默认只绑定快速问答和智能推理；可用 `--agents` 显式指定本空间其他智能体。保留模型、知识范围、其他技能和已有配置，并在 `.private/document-generation` 保存修改前备份。遇到已有不同沙箱时拒绝覆盖；安装失败或超时不绑定智能体。安装进度可在官方技能管理界面查看。

运行镜像包含中文字体、LibreOffice Writer、Poppler 和 Python 离线依赖仓库。命名沙箱使用 `network_mode=none`，安装和文档生成均不需要联网；会话 CPU、内存和 PID 仍有上限。每个会话只访问自身沙箱，下载继续验证会话所有权和空间权限。

服务器同样需要管理员配置，而非仅发布 Release：将镜像构建到该服务器的 Docker daemon（或从受控镜像仓库拉取），显式启用官方 Docker 后端并挂载 socket，然后按同一 API/UI 流程安装、绑定。也可使用已部署的 Cube/E2B 配置安装此技能及声明依赖。本次没有修改服务器配置或自动部署生产。

## 完成与失败

对话的结束标记代表流已经结束，不应当作业务任务已成功。达到轮数上限时，收尾回复必须是正常结束的文本；仍包含工具调用、流错误或被截断的回复不会作为最终结果。语义完整性由模型结合原请求、上下文及真实执行记录判断，不使用问题关键词。沙箱回复在检查前缓冲，失败会给出明确未完成说明，不把“稍后继续”当交付。

文件收集继续尽量保存成功文件；列表读取、单文件读取、超限或持久化失败会在实时交付路径返回错误，并在最终答复标明“文件交付未完成”。以实际附件列表及成功下载为准，不能仅凭模型说“生成成功”验收。

复核依据本轮最新请求界定任务，历史分析只作为上下文，不能把文件交付恢复成重做旧问题。沙箱执行后，服务器从当前会话的实际输出目录读取文件清单，只有清单证明的完整名称和路径才作为身份元数据处理；文件名中的日期和随机编号不当成统计数字。周围正文中的业务数值仍经过原有来源核验，未知文件名、失败结果或正文中嵌入的假清单不能取得这项身份。存在文件不等于正文事实已经全部正确，脚本和最终完成检查不能替代事实核验。

## 验证

`skills/document-export/tests` 检查中文、表格数值和来源保存、无效输入、路径越界、缺失转换器、真实双格式转换与重试保留旧文件。CI 安装 LibreOffice 和中文字体运行转换测试。Go 回归覆盖快速入口保留授权配置、语义核验、工具调用未结束、进度说明误结束、缓冲输出及产物持久化失败。

真实对话验收使用隔离会话复用原对话与真实工具证据；不修改原会话、不上传业务样例、不将验收数值写入生产代码。验收应分别检查模型实际使用技能、文件存在且可以下载、两个格式可以打开、原来源和关键值保留，并检查渲染页面。相关私有日志保存在 `.build/document-generation`。

2026-10-15 本地验收：复用用户原对话历史，在快速问答入口发送原来的后续请求，实际生成两个格式并通过带权限的下载接口取得文件；重新读取 Word/PDF，并独立核对原 Excel 中的九组最低值、日期和单元格。查看全部两页渲染结果，没有截断或缺字。排版器 13 项测试、智能体编辑器 16 项测试、部署及镜像计划 37 项测试通过；Go agent、session handler 全套与相关 service 回归通过。此记录不等同于对原回答全部业务解释的正确性保证，也不表示服务器已启用或部署。
