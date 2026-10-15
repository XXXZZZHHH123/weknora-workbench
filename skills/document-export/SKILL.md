---
name: document-export
description: 将对话中已获得依据的内容整理为可下载的 Word 或 PDF 文档，支持标题、段落、表格和来源说明。
---

# 文档交付

根据完整请求和上下文决定内容、范围和格式。复用当前任务已经取得的充分证据；仅在事实缺失、冲突或用户要求补充分析时继续查证。整理文档不意味着重新开展整轮研究。保留真实来源标题、工作表、页码、单元格、单位、实际覆盖范围和限制，不把历史回答中的未验证结论当成原始证据。

运行环境需要 Python 3.12、python-docx、pypdf、LibreOffice Writer 和中文字体。包内 requirements.txt 声明 Python 依赖；部署镜像 `deploy/Dockerfile.documents` 提供系统依赖。安装时确认这些依赖实际可运行，不能只登记技能名称。

文档镜像同时预置 `/opt/weknora/document-wheels` 离线依赖仓库。沙箱不需要联网；为技能创建独立虚拟环境时使用该离线仓库安装 requirements.txt，不要尝试在线下载或更改网络权限。验证 `libreoffice --version`、`pdftoppm -v`、Python 导入和脚本 `--help`。

用 `write_sandbox_file` 将模型整理的内容写为 `/workspace/document.json`。格式如下，字段可按内容省略：

```json
{
  "title": "文档标题",
  "subtitle": "范围或日期说明",
  "sections": [
    {"heading": "章节标题", "paragraphs": ["正文段落"],
     "table": {"headers": ["字段", "结果", "来源"], "rows": [["指标名称", "有依据的结果", "实际来源位置"]]}}
  ],
  "sources": ["真实文件及定位信息"]
}
```

这只是格式 schema，不是待填的固定报告模板。按请求选择章节、分析深度和表格，不加入无依据的业务内容。表格宜保持少量列，长表拆分，避免页面溢出。

执行：

```sh
python "$WEKNORA_SKILL_DIR/scripts/export_document.py" --input /workspace/document.json --name report --formats docx pdf
```

`--name` 使用与文档主题相关的文件名，不含路径和扩展名。用户明确选择格式时只传该格式；未指定时可交付 Word 和 PDF。脚本将同一份 Word 排版转换为 PDF，重新读取两个文件，成功后才在 stdout 返回包含真实路径、大小、SHA256、页数的 JSON 清单。生成失败会退出非零，不输出成功清单。

交付前检查返回清单及内容。可用 `pdftoppm -scale-to 1400 -png <pdf> /workspace/page` 渲染并检查布局。发现截断、缺字或表格溢出，应调整输入和重新生成。生成 Word 时也会内部转成 PDF 检查可打开性；实际要求的文件才写入 `/workspace/output`，中间 JSON、脚本和校验文件留在工作目录。

只有脚本成功、实际文件存在且系统能收集时，才在最终答复中提供 `/workspace/output/...` 的文件链接。未完成时明确说明具体失败步骤和原因，不能仅说“正在生成”后结束，不能将聊天正文冒充附件。
