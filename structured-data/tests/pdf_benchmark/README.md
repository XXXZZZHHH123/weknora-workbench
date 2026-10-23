# 独立 PDF 复杂结构基准

这些是隔离的合成测试材料，不含业务数据，不进入生产问答逻辑。`fixtures.py` 的作者数据是唯一金标准；不从 OCR/模型输出回填期望值。

## 固定范围：8 份 PDF / 12 页

| ID | 页数 | 作者定义的重点 |
|---|---:|---|
| `continued_three_pages` | 3 | 第2页重复表头，第3页无表头；空值、负数、零；同一逻辑表 |
| `split_text_row` | 2 | 页底/页首文字续行，数值只在上半行；显式参考续接操作 |
| `merged_headers` | 1 | 多行表头含 rowspan/colspan，正文有真实空值 |
| `body_spans_refusal` | 1 | 正文跨行/跨列，普通标量列映射必须拒绝 |
| `landscape_numeric` | 1 | 横向10列，逗号、百分号、正负号、dash、空值、脚注与上标 |
| `chinese_native_scan` | 1 | 原生中文表 + 嵌入140dpi/0.8°扫描中文表 |
| `formula_structures` | 1 | 真实分式、根式、求和、矩阵；附独立ID说明小表供表格桥接 |
| `different_units_blank_control` | 2 | 同形kg/lb独立表禁止拼接；第1页有作者定义的整行空白，第2页没有 |

金标准包括每个原格的原文、页码、零起算行列、单位、表头位置、合并范围、真实空白行和公式 LaTeX。原生格 bbox 表示作者定义的规范网格位置，合并格的完整区域需结合 `spans`，覆盖槽有 `covered_by` 标记；倾斜扫描格不伪造逐格坐标，仅给实际源页及保守整表区域。参考续表关系、禁止拼接关系、续行操作都显式记录，评分程序不会把它们当成已执行的模型判断。

## 生成与视觉核对

既有私有 venv 含 reportlab/Pillow/pdfium，无需安装。默认输出在仓库外 `private/pdf-complex-20261022`。字体默认使用该目录下的 `fonts/wqy-zenhei.ttc`（从现有开发 docreader 字体只读复制）及已有 dev cache 的 DejaVuSans；脚本不安装或下载字体。不要用未嵌入 STSong CID 字体替代：现场验证其文字层可提取但 pdfium 呈空白，不能作为有效中文图像来源。

```bash
wkdev tool /mnt/weknora-storage/weknora-dev/private/pdf-mineru-basic-20261022/venv/bin/python \
  structured-data/tests/pdf_benchmark/generate.py --output /absolute/private/new-benchmark-directory
```

可用 `--output`、`--cjk-font`、`--math-font` 指定其他目录/现有字体。已有 `golden.json` 的输出目录拒绝覆盖，须为新版本选新目录。PDF 固定 ReportLab invariant 模式，作者定义不依赖解析结果；`golden.json` 记录字体 SHA256、PDF SHA256、来源页格及期望结构。输出 `rendered/*.png` 和 `contact-sheet.jpg`，必须视觉确认字体、负号、公式、表格范围后再跑模型。

### 2026-10-22 字体缺陷纠正

v1 的 WQY 字体没有 U+2212 字形，`landscape_numeric` 的作者值 `−8.25` 在原 PDF 的文字层与像素中实际都退化为 `8.25`。这格的 v1 失配属于损坏的测试输入，不能归因解析器漏负号；初次缩略联系图检查漏看了此问题。原 v1/gold/run 全部保留。

生成器现按真实 cmap 检查每个表格字符，主字体缺字时明确使用有字形的 DejaVuSans，两者都没有时拒绝生成。已在独立 `private/pdf-complex-20261022-v2` 重新生成，只有 landscape PDF 的 SHA 改变，其余7份完全一致；高分辨率 crop 和 PDF 文字层均确认负号存在。比较解析器时须对该样例使用 v2 金标准和对应 v2 PDF，不能拿 v1 像素与 v2 期望混算。纠正证据分别在 v1 的 `landscape-font-audit-v1.json`、v2 的 `source-font-correction.json`。

`font-probe.pdf/png` 是初次 CID 字体失败的证据，不在 `golden.json` 的8份清单，不应混入12页基准。扫描辅助图保存在 `aux/`，同样不是额外验收 PDF。

## 明确调用真实解析服务

`inventory` 和 `score` 不联网；只有显式 `run` 才调用现有 bridge。脚本不启动/停止服务，不改应用环境，不安装模型。凭据只从私有 JSON 的 `api_key` 字段读取，没有 API key 命令行参数。

```bash
wkdev tool python structured-data/tests/pdf_benchmark/benchmark.py inventory

wkdev tool /mnt/weknora-storage/weknora-dev/private/pdf-mineru-basic-20261022/venv/bin/python \
  structured-data/tests/pdf_benchmark/benchmark.py run \
  --url http://127.0.0.1:18733 \
  --server-config /absolute/private/mineru.json \
  --fixtures merged_headers \
  --artifacts /absolute/private/pdf-complex-20261022/runs/unique-run
```

先以一页确认服务资源与任务耗时，再决定批量范围。省略 `--fixtures` 会串行跑8份；可一次列出多个 ID。默认每份1800秒，100次轮询覆盖总预算；可用 `--timeout` 收紧。默认遇传输/解析异常停止；`--continue-on-error` 才继续下一份。输出目录必须不存在，防止覆盖失败证据。`--tier basic|standard` 显式选择对比档位；不会重试其他协议、档位或模型。

每份成功保留 `*.raw.json`、`*.bundle.json`、`*.score.json`；`run-status.json` 包含每份传输状态/耗时，`golden.copy.json` 固定该轮作者金标准。错误只记录异常类别，避免 HTTP 异常混入认证值；需要具体失败原因时查解析服务的私有 job 日志。

离线重新评分：

```bash
wkdev tool python structured-data/tests/pdf_benchmark/benchmark.py score \
  --artifacts /absolute/private/pdf-complex-20261022/runs/unique-run
wkdev tool python -m unittest discover -s structured-data/tests/pdf_benchmark -p 'test_*.py' -v
```

## 捕获后的确定性发布验收

`publish_check.py` 只读取已经捕获的真实 bundle 与作者金标准，不会发起推理。它在独立目录写入测试草稿/发布记录，使用显式作者人工计划验证三页总和、跨页续行的多页 spans、正文合并格拒绝、不同单位分表和真实空白行。计划及预期只在本测试目录，不能进入生产业务分支。原始格/跨度不匹配金标准时不尝试发布。

```bash
wkdev tool python structured-data/tests/pdf_benchmark/publish_check.py \
  --artifacts /absolute/private/pdf-complex-20261022/runs/standard-full \
  --output /absolute/private/new-publication-check-directory
```

这些检查证明“已识别原格 + 显式人工计划 → 确定性发布/计算”的行为，不证明模型已自动判断正确的业务续表或单位绑定。

## 评分边界

- 表格按真实页码与作者 bbox 配对；无 bbox 时按同页输出顺序，不按最大文字匹配分数挑表。匹配后严格比较原始行列，不对齐/删除/补造数据行。缺行、额外空行、漏列、跨度丢失、页码错均独立报告。
- 文字只规范空白、HTML实体、Unicode负号等价形式和上标表示；不会去逗号/百分号、丢负号、补零、做单位换算或把 `10²` 等同 `102`。真实空白行必须位置一致。
- 公式独立于表格评分；仅忽略LaTeX空白、最外层数学定界符和 `\left/\right`。不把扁平 `a+b/c-d` 当作分式，不做代数等价证明。等价但不同LaTeX排版仍可能被严格评分判错，需要人工逐项复核，不得据此声称数学语义一定错误。
- 汇总中的 cell/formula 分母是**已拿到 bundle 的样例**；同时查看 `scored_fixture_count` 与 `run-status.json`，失败/未执行文件不能被忽略后声称全套通过。
- HTTP成功不是识别正确。脚本始终 `acceptance_complete=false`，明确未执行续表模型决策、人工审校、结构化发布或最终问答；作者的禁止合并/正文跨度拒绝要求仍需单独验证产品行为。公式结构分数与数值/表格分数不得合并成一句“全部通过”。
