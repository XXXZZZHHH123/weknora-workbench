# 研究工作台界面文案审计

盘点基线 `4e2cd87c`，日期 2026-11-23。范围包括登录、知识库、资料处理、对话与引用、表格分析、商品研究、周报、设置和部分辅助工具。下表以源码中同一处展示行为为一条；页面内重复使用的文案合并记录，语言包另外逐键列出。主要页面和语言包使用改后行号。指向原始接口错误的位置沿用盘点定位。API 原有字段、状态码、业务计算及来源权限照常使用。

## 登录、邀请与空间

| 文件:行号 | 改前 | 改后 | 说明 |
|---|---|---|---|
| `WeKnora/frontend/src/views/auth/Login.vue:425`、`WeKnora/frontend/src/App.vue:131` | 登录跳转、回调使用 OIDC 或身份提供商失败原因 | 使用单位账号登录；正在打开单位登录页面；暂时无法完成单位账号登录，请重试，仍失败时联系空间管理员 | 保留已配置的单位登录平台名称。 |
| `WeKnora/frontend/src/utils/request.ts:162`、`WeKnora/frontend/src/utils/userFacingError.ts:1` | 后端优先显示 Invalid login parameters、Login failed、token is required、Invalid registration parameters | 请填写邮箱和密码；邮箱或密码不正确；注册链接不完整时从邀请邮件重新打开；修改漏填或已被使用的注册资料 | 表单已有各输入框校验。保留原始错误用于排查。 |
| `WeKnora/frontend/src/views/auth/Login.vue:505` | 注册失败／注册错误请稍后重试／原始异常 | 已知错误说明如何改。无法确认时先提示尝试登录，仍无法进入时再重新注册 | 降低重复提交。 |
| `WeKnora/frontend/src/api/tenant/index.ts:223`、`WeKnora/frontend/src/api/tenant/index.ts:209` | 创建空间、读取或创建/删除密钥的后备翻译键缺失 | 核对空间名称或联系管理员获取邀请；失败时先刷新密钥列表核对 | 补齐语言包键。 |

## 知识库、资料中心与处理进度

| 文件:行号 | 改前 | 改后 | 说明 |
|---|---|---|---|
| `WeKnora/frontend/src/components/upload-tasks/UploadTaskRow.vue:55`、`WeKnora/frontend/src/utils/knowledgeIngestion.ts:34` | 处理流程已结束；分块计数；表格数据区块、识别片段；原始任务异常 | 正文可搜索时说明其他内容仍在整理；列出已保存的正文段数、可查询的表格区域、可计算的数值和待核对项 | 不从页数推断收录范围；目录计数、自动整理、人工核对分开。上传失败和原件已保存但处理未完成分开提示。 |
| `WeKnora/frontend/src/views/materials/intake.ts:24`、`WeKnora/frontend/src/views/materials/MaterialsCenter.vue:64` | 待确认／可恢复／处理失败／任务异常原文 | 请确认表格范围或指标；处理已中断、可恢复；按用途说明失败影响并提示重试 | 有原件时继续提供下载。管理员可以展开任务异常。 |
| `WeKnora/frontend/src/views/materials/MaterialsCenter.vue:71` | 标题缺失时显示文档编号；其余状态统一处理中 | 未命名文档；等待处理、正在解析、正文可搜索、处理已停止或暂时无法确认 | 把等待与实际开始处理区分开。 |
| `WeKnora/frontend/src/views/materials/MaterialsCenter.vue:381` | 查看文件指纹 | 原文件名称、大小、上传时间、下载按钮 | 时间显示到分钟，校验值移入管理员详情。 |
| `WeKnora/frontend/src/views/research/IsolatedResearchWorkbench.vue:160`、`WeKnora/frontend/src/views/research/IsolatedResearchWorkbench.vue:221` | 来源编号、短哈希、未识别的处理状态、结构化解析 | 文件名或未命名资料；保存时间；整理并核对表格；已保存／等待处理／正文可搜索 | 文件版本身份仍由程序校验，编号和哈希供管理员排查。 |
| `WeKnora/frontend/src/components/doc-content.vue:1648`、`WeKnora/frontend/src/utils/userFacingError.ts:15` | file.downloadFailed；文档 ID 不能为空 | 未能下载原件，请从知识库重新打开这份文档后再试 | 下载途径和错误协议原样使用。 |
| `WeKnora/frontend/src/components/knowledge-processing-timeline.vue:1017`、`WeKnora/frontend/src/views/knowledge/components/DocumentActionMenu.vue:65` | 处理流水线、LIVE、查看 Trace，未知步骤和状态显示枚举或翻译键 | 文件处理进度、正在更新、查看处理进度；其他处理步骤或暂时无法确认进度 | 各步骤说明在处理什么。时间显示到分钟，显示从处理开始算起的时长。 |
| `WeKnora/frontend/src/components/knowledge-processing-timeline.vue:1125`、`WeKnora/frontend/src/components/knowledge-processing-timeline.vue:1909` | true／false、Array／Object、起点偏移、关联编号和 JSON | 已开启／未开启、共若干项；一般详情只展示步骤、状态和时间 | 原始记录、字段和可复制诊断仅管理员可见。 |
| `WeKnora/frontend/src/components/knowledge-processing-timeline.vue:1255`、`WeKnora/frontend/src/components/knowledge-processing-timeline.vue:1685` | 原样展示错误码、DocReader 异常、磁盘或队列提示 | 文件服务暂不可用、读不完文件、解析空间不足及长期无进展已停止；按原因提示重新解析或联系管理员 | 仍在等待时说明可继续等或停止后重新解析。 |
| `WeKnora/frontend/src/components/knowledge-processing-timeline.vue:1971` | 没有详细 span，只显示推断状态 | 这一步没有单独的进度记录，页面根据整体进度估计 | 区分实际记录和估计。 |

## 表格分析入口和指标写入

| 文件:行号 | 改前 | 改后 | 说明 |
|---|---|---|---|
| `WeKnora/frontend/src/views/structured/index.vue:3`、`WeKnora/frontend/src/views/workspaces/KnowledgeStructuredData.vue:61` | 完整数值、字段与原单元格／结构化数据工作台／字段模型 | 表格分析，按指标查询数值，也可以回到原表核对；表头识别模型 | 入口说明能做的事。 |
| `WeKnora/frontend/src/views/materials/MaterialsCenter.vue:247`、`WeKnora/frontend/src/views/materials/MaterialsCenter.vue:264` | 字段目录不完整、分页异常或接入失败 | 指标列表没有读全，请重新读取表格字段；尚未加入研究数据时提示重新预览并确认 | 保留原处理流程。 |
| `WeKnora/frontend/src/views/materials/MaterialsCenter.vue:384`、`WeKnora/frontend/src/views/materials/MaterialsCenter.vue:385` | 来源冲突、观测修订、映射及裸露指标编号 | 对照同日期的旧值和新值，再确认保存为新版本；指标名缺失时请为该列选择指标 | 保留日期、单元格、已有值、本次值和目标单位。已有可逐行勾选的流程仍按原选择保存。 |
| `WeKnora/frontend/src/utils/userFacingError.ts:22`、`WeKnora/frontend/src/components/KnowledgeStructuredFrame.vue:42` | 结构化服务未配置、知识库 ID 无效、需要成员身份或限定参数 | 用有权访问该知识库的账号打开；联系管理员设置表头识别；重新打开表格或选择模型和范围 | 表格页使用已有同源通信按管理员身份开关诊断详情。 |

## 研究工作台提示

| 文件:行号 | 改前 | 改后 | 说明 |
|---|---|---|---|
| `WeKnora/frontend/src/components/ResearchAnalysisTasks.vue:50`、`WeKnora/frontend/src/api/researchAnalysisTasks.ts:75` | 产业图配置已生成、结果已暂存尚未保存、忽略状态未能核对 | 产业图草稿已生成；点查看结果核对并确认保存；无法确认是否忽略时刷新任务列表 | 说明生成和采用结果是两个步骤。 |
| `WeKnora/frontend/src/views/research/researchFeatureGuides.ts:70`、`WeKnora/frontend/src/utils/userFacingError.ts:28` | 真实指标目录、Default 宏观指标、128 份表格原件容量且未静默截断 | 已导入指标、管理员配置的公共宏观指标；本次最多使用 128 份表格原件 | 保留已配置的容量边界。 |

## 搜索、模型和同步设置

| 文件:行号 | 改前 | 改后 | 说明 |
|---|---|---|---|
| `WeKnora/frontend/src/components/ModelDebugDrawer.vue:140` | Max Tokens | 单次回复长度上限（token） | 悬浮说明解释单位。 |
| `WeKnora/frontend/src/views/settings/PDFProcessingSettings.vue:55` | 相邻批次重叠、实际解析时限、接口解析对象或保存失败 | 相邻两批重复读取的页数、每批实际处理最长时间；核对页数和时长后重试 | 管理员可展开原始失败原因。 |
| `WeKnora/frontend/src/views/settings/ParserEngineSettings.vue:243`、`WeKnora/frontend/src/views/settings/ParserEngineSettings.vue:696` | API Key、Model Version、第三方不可用原因 | 服务密钥、模型版本，并提示按供应商文档填写；测试失败后核对服务地址、密钥和模型 | 管理员工具提示保留供应商正式名称；原始结果可展开。 |
| `WeKnora/frontend/src/views/knowledge/settings/DataSourceSettings.vue:120`、`WeKnora/frontend/src/views/knowledge/settings/DataSourceSyncLogs.vue:136` | 按钮显示后备键，错误记录回显字段键、枚举或原始异常 | 同步尚未恢复，请重试；显示受影响的文件及已知原因，未知原因提示核对来源和权限 | 保留逐条失败记录和管理员诊断。 |
| `WeKnora/frontend/src/views/knowledge/components/FAQEntryManager.vue:2507`、`WeKnora/frontend/src/utils/userFacingError.ts:20` | 导出提示显示翻译键、错误显示标签或问答 ID 参数 | 请先选择要导出的知识库；重新选择标签或问答 | 保留常见问答权限与操作方式。 |
| `WeKnora/frontend/src/views/settings/StorageBackendSettings.vue:343`、`WeKnora/frontend/src/utils/userFacingError.ts:16` | Bucket 格式、Endpoint 和 Access Key 组合、AWS 默认凭证链错误 | 核对存储空间名称和服务地址。按认证方式补齐访问密钥，或检查服务器 AWS 身份的访问权限 | 保留供应商正式字段名。失败原文收在管理员详情。 |

## 账号、空间、合作与项目对话

| 文件:行号 | 改前 | 改后 | 说明 |
|---|---|---|---|
| `WeKnora/frontend/src/components/TenantSelector.vue:40`、`WeKnora/frontend/src/views/settings/UserProfile.vue:25` | 空间 ID、个人用户 ID 和 OIDC | 空间名称和现有说明；单位账号；排查时由管理员查看用户编号 | 重名且无说明时不会编造组织名称或负责人。 |
| `WeKnora/frontend/src/components/ResourcePermissionDrawer.vue:177`、`WeKnora/frontend/src/components/TenantNotificationsDrawer.vue:17` | 授权和通知用编号代替姓名 | 优先显示姓名或已有邮箱；姓名暂不可用时提示刷新核对 | 授权与撤销的操作对象保持原来的选择值。 |
| `WeKnora/frontend/src/views/workspaces/WorkbenchSourcesDialog.vue:58`、`WeKnora/frontend/src/views/workspaces/KnowledgeWorkbenchLink.vue:39` | 相关指标绑定不可用、按需加载、重试工作台绑定 | 已有研究成果仍可查看，依赖来源的指标无法更新；重新查看关联的工作台 | 说明移除来源的业务影响。 |
| `WeKnora/frontend/src/views/workspaces/AssistantProjects.vue:135`、`WeKnora/frontend/src/utils/localDateTime.ts:2` | 直接显示带时区和秒的接口时间 | 更新于或创建于 YYYY-MM-DD HH:mm | 根据浏览器时区显示，不改接口时间。 |

## 辅助工具和通用异常

| 文件:行号 | 改前 | 改后 | 说明 |
|---|---|---|---|
| `WeKnora/frontend/src/views/automation/AutomationDetail.vue:69`、`WeKnora/frontend/src/views/automation/AutomationDetail.vue:158` | 工具内部名、原始运行异常、Cron 表达式作为任务名片 | 未命名的任务操作；任务未完成，请查看任务输入后再运行；自定义定时规则 | 编辑入口保留高级规则和运行时间预览；运行异常供管理员查看。 |
| `WeKnora/frontend/src/i18n/embed.ts:63`、`WeKnora/frontend/src/composables/useEmbedBridge.ts:183` | 缺少嵌入渠道或 Token、等待宿主页面提供 Token、原样显示连接异常 | 正在连接此网站的研究助手；无法打开时请联系网站管理员 | 简繁独立嵌入包同步。 |
| `WeKnora/frontend/src/components/IMChannelPanel.vue:263` | Bot ID、Bot Secret、WebSocket Endpoint、Verification Token 等 | 机器人编号、机器人密钥、长连接地址、消息验证令牌等 | 渠道管理员可以继续对照供应商字段；密钥显示方式不变。 |
| `WeKnora/frontend/src/views/system/RuntimeQueues.vue:414` | 工作进程编号和任务异常直接显示 | 任务未能完成，请查看受影响的资料后重试 | 工作进程编号及错误移入诊断详情。 |
| `WeKnora/frontend/src/utils/request.ts:228`、`WeKnora/frontend/src/components/ErrorDiagnostics.vue:15` | 通用异常恢复的英文或文本响应原样进入提示 | 读取失败请刷新，写入结果不明时先刷新核对，再决定是否重试 | 保留响应中的业务错误和协议字段；本次盘点的已知错误由统一展示映射处理。 |

## 对话问答、引用与取数依据

| 文件:行号 | 改前 | 改后 | 说明 |
|---|---|---|---|
| `WeKnora/internal/agent/numeric_occurrences.go:21`、`WeKnora/internal/agent/numeric_unverified_delivery.go:90` | 〔未核验〕；未能与本轮取数结果逐项核对 | 〔数字待核对〕；本次查到的数据还不能确认带标记的数字。写入报告前，请对照原表或原文核对。 | 能列出的待核对数值仍在尾注中。原核对条件、引用验证和数字计算保持原样。 |
| `WeKnora/internal/agent/citation_occurrences.go:95`、`WeKnora/internal/agent/citation_unverified_delivery.go:278` | 〔引用未核验〕；已移除对应来源入口 | 〔引用待核对〕；本次读取的资料未能确认带标记的引用，因此没有可打开的来源链接。请找到对应原文后再引用。 | 数字也待核对时保留独立行内标记与尾注说明。 |
| `WeKnora/frontend/src/components/MessageEvidencePanel.vue:156` | 查看来源，供核查；回答尚未完成自动审核 | 查看本次查到的数据；带标记的说法仍待核对，请对照原表或原文确认。 | 提示跟随证据状态，简繁中文同步。 |
| `WeKnora/frontend/src/components/MessageEvidencePanel.vue:74`、`:173`、`:178` | 已保存的执行结果与原件版本、声明层级、单位分类枚举 | 回答时使用的数据和原件版本；单位从哪里得出。来源说明写成表格设置、导入时的指标说明或计算公式。 | 找不到依据时明确提示核对原表。单位和业务表达式保留。 |
| `WeKnora/frontend/src/components/MessageEvidencePanel.vue:35`、`:182` | 加载的输入数；实际读入范围原始枚举、覆盖说明对象 | 本次读取的记录数；使用筛选和计算前读到的记录，或使用本次查询返回的记录。 | 仅对已知范围按实际含义显示；覆盖原数据保留在管理诊断。 |
| `WeKnora/frontend/src/components/MessageEvidencePanel.vue:189`、`WeKnora/frontend/src/utils/evidenceSources.ts:63` | 实际绘图映射（原表坐标从0起算）；对象转 JSON | 横轴、纵轴及单位、来源文件、工作表及已保存的 Excel 单元格范围。 | 图表列顺序沿用返回的绘图数据；精确坐标缺失时提示打开原件。原始映射放管理诊断。 |
| `WeKnora/frontend/src/components/MessageEvidencePanel.vue:134` | 复合单元格直接展示 JSON；时间原样显示 | 按已知的数值、单位和来源显示；未知结构提示查看原件。 | 数值字符串保留原精度，带时区的时间显示为本地日期和分钟。普通详情不暴露对象键。 |
| `WeKnora/frontend/src/components/MessageEvidencePanel.vue:57`、`:169` | 历史证据未保存原件版本，无法确认原定位；原件版本核验超过单次预览范围；来源已更新或原件版本无法读取 | 分别说明旧回答没有保存当时的文件版本、预览无法核对完整文件、无法打开回答时使用的版本。 | 前端按已有返回原因映射；原文保留在管理诊断。 |
| `WeKnora/frontend/src/components/chat/StructuredAnalysisEvidence.vue:4`、`:11`、`:18`、`:34` | 查看分析取数与计算依据；不代表完整 SQL 行级血缘；输入覆盖不完整；来源版本 SHA-256 | 查看数据范围和计算依据；以下单元格仅供抽查；本次只读到部分数据，结果不能用于全表统计。 | 可核对工作表、日期、有效记录和原单元格。查询语句和校验值放管理诊断。 |
| `WeKnora/frontend/src/utils/structuredSourceRead.ts:43`、`WeKnora/frontend/src/components/chat/StructuredAnalysisEvidence.vue:221`、`:228`、`:236` | 无权读取或登录过期共用文案，返回 HTTP 状态、文件哈希和原始异常 | 登录已失效，请重新登录后继续；无权读取原件，请联系资料负责人；版本不同请重新选择原件；无页码请打开 PDF 查找表名。 | 原始诊断按管理员显示；原件身份、页码和读取范围校验继续执行。 |
| `WeKnora/frontend/src/components/ChatRequestInfoButton.vue:20`、`:85` | 请求信息、请求方法和地址、三个内部编号 | 回答信息，显示提问时间和可查看的来源数量。 | 有权限的管理员可以复制诊断信息；提问时间按本地时间显示到分钟。 |
| `WeKnora/frontend/src/views/chat/components/tool-results/RelatedChunks.vue:25`、`WeKnora/frontend/src/views/chat/components/tool-results/ContentPopup.vue:19`、`WeKnora/frontend/src/views/chat/components/tool-results/ChunkDetail.vue:3`、`WeKnora/frontend/src/views/chat/components/tool-results/DocumentInfo.vue:23` | 片段序号、内部位置、片段/文档/问答编号 | 用摘录识别原文，显示文档标题及问答正文。 | 内部编号和位置在管理员可展开内容中查看。 |
| `WeKnora/frontend/src/views/chat/components/tool-results/GraphQueryResults.vue:4`、`:35` | 裸百分比、图谱配置 | 保留相关程度和资料标题。 | 分数挪入管理员诊断，普通界面保留相关程度。现有展示顺序保持原样。 |
| `WeKnora/frontend/src/views/chat/components/ToolApprovalCard.vue:10`、`:237`、`WeKnora/frontend/src/views/chat/components/tool-results/McpToolResult.vue:60` | 键名=值，完整参数定义 | 优先显示人可读的操作内容、已知输入名称和值；定义视图使用“文字”“数字”“是或否”等标签。 | 通用工具的确切授权输入在授权者主动展开时可查看或修改，完整定义仅供管理员排查。 |
| `WeKnora/frontend/src/views/chat/components/tool-results/ShellExecResult.vue:3`、`:18`、`WeKnora/frontend/src/views/chat/components/tool-results/WebFetchResults.vue:55`、`:69` | 显示标准错误、命令状态码、网页读取和摘要异常原文 | 本次操作未能完成，请核对输入后重试；网页未能读取和摘要未生成分别提示。 | 原始诊断可展开，直接阅读的网页原文和正常返回的结果保留。 |
| `WeKnora/frontend/src/utils/fieldClarification.ts:130`、`:139` | 工具传入的工作表范围无效、目录超出处理范围、请展开调用详情 | 这次没有取得数值。请指定文件和工作表后再查；找不到工作表或缺少文件时提示对应操作。 | 根据已有通知状态映射，由模型判断查询意图的流程保持原样。 |
| `WeKnora/frontend/src/components/chat/FieldClarificationDialog.vue:16`、`:25`、`:41` | 标签可能是 v1；请为每项用途选择一个字段 | 尽量显示问题中的指标名或候选表头；请选你指的指标，确认后开始查询。 | 一项无法命名时提示确认指标，多项按显示顺序区分。 |
| `WeKnora/frontend/src/components/chat/StructuredFieldAnswer.vue:11`、`:62`、`:77` | 部分表头证据、左侧原值和右侧原值、模型提出业务口径 | 提醒打开原表查看其余列；对比列用指标名、单位和同名时的工作表；提示核对公式、原单位和目标单位。 | 保留原对比顺序、原数值和来源单元格。 |
| `WeKnora/frontend/src/views/chat/components/botmsg.vue:512`、`:523`、`:539`、`WeKnora/frontend/src/views/chat/components/tool-results/GrepResults.vue:60` | 缺少前端语言包的后备键 | 分别提醒当前没有可复制或保存的正文，打开编辑器后选择知识库再保存；未命名文档。 | 复用已有的未命名文档翻译。 |
| `WeKnora/frontend/src/views/chat/components/BackgroundWorkChip.vue:22` | 无标题后台操作用工作编号 | 后台任务，或后台运行的操作。 | 操作按钮继续绑定原任务。 |

## 外部服务工具设置

| 文件:行号 | 改前 | 改后 | 说明 |
|---|---|---|---|
| `WeKnora/frontend/src/views/settings/components/McpToolsList.vue:67`、`WeKnora/frontend/src/utils/mcpToolDisplay.ts:126` | 参数类型直接显示 string、integer 等 | 文字、数字、是或否、列表和一组信息。 | 管理设置的悬浮说明保留协议字段类型。 |

### 本组盘点中有意保留的内容

- 外部工具可以定义任意操作。授权者主动展开后仍能核对和编辑完整输入，避免在确认读取、修改或发送前丢失对象及范围。已有工具说明和已知参数优先显示，未知参数的摘要只写“第几项输入”。
- 历史回答已经保存的正文、原始文件、用户命名、外部服务提供商给工具取的名称及正文原文保留原貌。历史文档中的旧标记仍是当时保存的内容。
- 分析证据的历史计算只保存部分原单元格位置，界面按实际可用的日期、指标、工作表和单元格说明。缺少结构化筛选条件或业务算式时未从查询语句推测；管理员仍可查看查询诊断。
- 两条 Go 引用及数字文案通过修改纯显示文本实现，保持 API 和已有状态不变。Go 包全量测试被沙箱阻止使用临时监听端口，相关交付、正文字段及核对测试通过。

## 表格分析工作台

### 表格分析入口、登录与识别模型

| 文件:行号 | 改前 | 改后 | 说明 |
|---|---|---|---|
| `structured-data/src/structured_data/index.html:74` | 表格分析，标题下没有用途说明 | 按指标查询数值，也可以回到原表核对。 | P0，说明查询和核对两个用途。 |
| `structured-data/src/structured_data/index.html:77` | 字段模型／平台通用模型 | 表头识别模型；用工作台已有的模型识别表头、指标和单位，结果可以人工核对。 | P0，说明模型使用场景。 |
| `structured-data/src/structured_data/index.html:136` | 登录表单直接显示接口错误 | 请填写邮箱和密码后登录。／邮箱或密码不正确，请核对后再试。／暂时无法登录，请稍后重试。 | P0，区分输入、登录未通过和服务不可用。 |
| `structured-data/src/structured_data/index.html:142` | 原样显示登录刷新失败原因和权限错误 | 登录已失效，请重新登录后继续。／你没有这项操作的权限，请确认所选空间和知识库，或联系管理员。 | P0，按实际响应区分。 |
| `structured-data/src/structured_data/index.html:647` | 当前仅可查看，需知识库编辑权限才能更换模型。／当前沿用原字段模型；选择后将改用平台配置。 | 你可以查看当前模型。如需更换，请联系有知识库编辑权限的人。／当前沿用之前的表头识别模型。保存后，下次识别会使用你选择的模型。 | P0，解释操作权限、生效时机和默认行为。 |
| `structured-data/src/structured_data/index.html:653` | 下一次 AI 辅助和字段理解将使用此模型。 | 下次识别表头和推荐指标时，会使用所选模型。 | P0，明确影响的操作。 |

### Excel 版本、指标查询与图表

| 文件:行号 | 改前 | 改后 | 说明 |
|---|---|---|---|
| `structured-data/src/structured_data/index.html:88`、`:90`、`:285` | 验收样例扫描记录／验收样例 | 示例文件处理记录／示例数据 | P0，继续单独列出示例，不混入业务数据。 |
| `structured-data/src/structured_data/index.html:296`、`:309`、`:315`、`:420` | 短数据集编号、接口日期或本地时间含秒 | 文件名、导入时间、可查询表数。时间按本地时区显示为 YYYY-MM-DD HH:mm。 | P0，选择值保留在程序内，日期型数据保留原日期。 |
| `structured-data/src/structured_data/index.html:92`、`:353` | 指标标识及只读的 `series_id` | 指标来源详情显示文件、工作表、原列名（如有）、表头位置和单位。 | P0，查询仍使用原指标选择值。 |
| `structured-data/src/structured_data/index.html:92` | 期间／版本标签（精确匹配） | 原表中的期间或版本 | P0，保留按原表标签查找。 |
| `structured-data/src/structured_data/index.html:150` | 数据集 ID 无效，请先调用 list_datasets。 | 所选文件版本已不可用，请重新选择文件。 | P0，在页面统一错误层改写，保留接口返回。 |
| `structured-data/src/structured_data/index.html:151` | 指标不存在，请调用 list_series 取得精确 series_id，不能猜测。 | 这个版本中找不到可用的所选指标，请刷新指标列表后重选。 | P0，图表指标失效使用同一提示。 |
| `structured-data/src/structured_data/index.html:126`、`:146`、`:236` | 原样显示接口错误或将 `detail` 输出为 JSON | 按对象显示查询、读取和保存失败；校验错误点出中文控件，表格错误补充单元格。 | P0，管理员可展开排查详情。校验对象保留报错类别、位置和说明，排除请求输入值。 |
| `structured-data/src/structured_data/index.html:336` | 文件指纹 | 已处理的工作表和该版本原文件下载入口 | P0，版本由文件与导入记录识别。 |
| `structured-data/src/structured_data/index.html:368`、`:398` | 数据与原单元格证据／返回行数已截断／数值状态枚举 | 查询数值与原表位置／当前只显示部分记录，可以缩小日期范围后再查。／数值状态待核对 | P0，原始数值与原表来源仍可核对。 |
| `structured-data/src/structured_data/index.html:374`、`:383`；`structured-data/src/structured_data/sheet-review.js:547` | 原生图表／此范围没有原生图表。／图表和曲线的未知状态枚举 | Excel 自带图表／所选范围没有 Excel 文件自带的图表。／图表状态待确认，请查看原工作表。 | P0，查询面板与工作表图表卡片共用状态文案。 |
| `structured-data/src/structured_data/index.html:392` | 原序列代码／内部表区编号／原始维度键 | 原表指标代码／表格区域和业务分类值 | P0，已知类别有中文说明，省略内部区域号。 |
| `structured-data/src/structured_data/sheet-review.js:432` | 公式使用原文件缓存值；空白、错误或无缓存值保持缺失。 | 公式显示文件上次保存的计算结果。空白、报错和没有已保存结果的单元格记为缺失。 | P0，说明口径并保留单元格入口。 |
| `structured-data/src/structured_data/sheet-review.js:443` | 本次导入当前可用版本及短数据集编号 | 原文件、导入时间 | P0，来源字段位置和单位继续显示。 |

### Excel 上传与核对

| 文件:行号 | 改前 | 改后 | 说明 |
|---|---|---|---|
| `structured-data/src/structured_data/index.html:172`、`:515`；`structured-data/src/structured_data/sheet-review.js:749` | 扫描全部工作表并导入结构明确的表，不调用模型／校验当前工作表并增量保存，复用其他已导入工作表 | 正在逐张检查工作表，格式清楚的表会自动导入。／正在检查并保存这张工作表，其他工作表沿用已有结果。 | P0，在展示层兼容已保存的进度文案。 |
| `structured-data/src/structured_data/index.html:399`、`:411` | 未知状态枚举、无法理解的预览 JSON | 状态暂时无法确认；暂时无法查看预览，请打开原表核对。 | P0，仅展示可解释的预览值。 |
| `structured-data/src/structured_data/index.html:515`；`structured-data/src/structured_data/sheet-review.js:749`、`:957` | 处理未完成／原样展示后台错误和批量失败说明 | 《工作表名》未能完成处理，请返回核对后重试。／请核对所标明的控件和原表单元格后再试。 | P0，保留现有定位、部分成功结果和取消逻辑。具体原始异常仅出现在管理员排查详情。 |
| `structured-data/src/structured_data/index.html:568`；`structured-data/src/structured_data/sheet-review.js:896` | 正在保存映射并生成数据集新版本／正在生成数据集新版本 | 正在保存列与指标的对应关系，并更新可查询的数据… | P0，完成时提示所选区域已导入。 |
| `structured-data/src/structured_data/index.html:628` | 上传后报错直接显示异常 | 原件已保存，表格处理未完成。请先刷新处理结果，再重试处理。 | P0，仅在已拿到保存回执时使用。原件保存结果未知时提示先刷新知识库。 |
| `structured-data/src/structured_data/sheet-review.js:737` | AI 已进入保存阶段，暂时不能取消：服务返回原文 | 暂时无法取消，请等待本次处理结束。 | P0，取消请求未获确认时不假定已进入保存。 |
| `structured-data/src/structured_data/sheet-review.js:924` | 逐表校验并导入，成功快照、原文件和历史版本保留 | 将逐张检查并导入工作表。未通过的表仍可核对，已导入的数据、原件和历史版本会保留。 | P0，说明部分成功和对已有内容的影响。 |
| `structured-data/src/structured_data/sheet-review.js:934` | 逐表重新执行现有检查，通过后才确认已保存的映射。 | 将逐张核对所选工作表，通过一张就确认一张。未通过的表会列出原因。尚未保存的修改请回到对应工作表核对并导入。 | P0，明确范围、失败去向和草稿。 |

### PDF 处理、核对与查询

| 文件:行号 | 改前 | 改后 | 说明 |
|---|---|---|---|
| `structured-data/src/structured_data/static/pdf-review.js:145` | 计划必须是有效的 JSON 对象。 | 暂时无法读取刚才的表格修改，请重新打开这张表后再试。 | P0，保留隐藏的编辑数据和校验。 |
| `structured-data/src/structured_data/static/pdf-review.js:653` | 请先选择至少一个原始表片段。／所选片段列数不一致，不能生成无损手动模板。 | 请先选择要拼接的表格区域。／所选区域的列数不同，请对照原 PDF 核对它们是否属于同一张表，再调整选择。 | P0，说明失败原因及补救。 |
| `structured-data/src/structured_data/static/pdf-review.js:490`、`:629` | 本地时间含秒／记录信息显示内部编号 | 识别时间显示为 YYYY-MM-DD HH:mm，历史条目通过文件版本、时间与表数区分。 | P0，原件入口仍可用。 |
| `structured-data/src/structured_data/static/pdf-review.js:80`、`:531`、`:1019` | 错误代码、任务异常在列表或普通详情中显示 | 普通页面显示受影响的文件、已知原因和下一步，管理员可展开排查详情。 | P0，降权或换空间时清理旧详情。 |
| `structured-data/src/structured_data/static/pdf-review.js:1250` | PDF 任务：状态枚举／未知的 PDF 任务状态 | 等待处理／正在识别 PDF 中的表格／正在整理表格／暂时无法确认处理进度，请刷新记录。 | P0，未知状态原值只用于排查。 |
| `structured-data/src/structured_data/static/pdf-review.js:1276` | 解析完成但没有可读取的 PDF 草稿编号。 | PDF 已识别，但暂时打不开核对结果。请刷新处理记录，再打开这份文件。 | P0，指引到处理记录。 |
| `structured-data/src/structured_data/static/pdf-review.js:1303`、`:1389` | PDF 原件编号为空／PDF 解析服务尚未配置 | 暂时找不到 PDF 原件，请从知识库重新打开这份文件。／暂时无法处理新 PDF，请联系管理员开通文件识别。 | P0，区分文件入口和配置问题。 |
| `structured-data/src/structured_data/pdf-query.js:43`、`:51`、`:100`、`:109` | 原页出处／此表已自动入库／计算覆盖筛选后的全部数据；此表共载入若干行 | 原文页码／这张表由系统识别，请对照原件核对。／计算使用了所有符合筛选条件的数据，当前表格共若干行，计算会跳过空白处。 | 低风险 P2，只改展示文案。 |

### 表格分析有意保留的盘点项

| 文件:行号 | 条目 | 原因 |
|---|---|---|
| `structured-data/src/structured_data/field_resolver.py:712` | 字段角色和缺省标签 `v1` 等 | 角色用于校验模型方案和执行计算，界面在 WeKnora 的字段确认弹窗改写该回退。 |
| `structured-data/src/structured_data/server.py:787`、`:951`、`:1157`、`:1523`；`structured-data/src/structured_data/store.py:174`、`:619` | API 层的内部错误和进度字符串 | 浏览器统一映射，兼容旧记录及其他协议调用方。 |
| `structured-data/src/structured_data/static/pdf-review.js:1104` | PDF 表格的编辑计划、字段键和记录编号 | 编辑控件使用隐藏的数据，保留现有保存和核对流程。 |

## 商品研究与周报

### 商品研究｜资料上传与原件核对

| 文件:行号 | 改前 | 改后 | 说明 |
|---|---|---|---|
| `commodity-research/src/tin/web/templates/materials.html:18–26` | `k.title`、`k.file_name` 缺失时显示内部编号，解析状态直接显示 `parse_status`，上传后显示 SHA256 | “未命名资料”“等待处理／正在解析／已可搜索，仍在整理／已可用／解析未完成”。成功后显示文件名和“原件已保存”，请到列表查看正文何时可搜索 | 等待上传结果与解析完成分别提示。文件校验值和错误原文在验证过的管理员详情中查看 |
| `commodity-research/src/tin/web/templates/sources.html:9–32` | 研究字段接入／映射说明／确认映射并导入／允许登记新编码 | 把表格数据加入研究指标／这列数据对应哪个指标／确认对应关系并导入／同意新增这些指标 | 保留已核对的数据列、单位与冲突确认流程 |
| `commodity-research/src/tin/web/templates/sources.html:38–46、:75` | 文件类型枚举、字节数、截短编号、SHA256、下载不可变原件 | 文件名、中文文件类型、KB 或 MB、保存日期与时间、下载当时上传的原件 | 原件版本和技术响应仅在管理员详情显示，下载失败保留页面并说明怎么重试 |
| `commodity-research/src/tin/web/templates/sources.html:44、:79`；`commodity-research/src/tin/provenance/service.py:217` | 缓存值／`formula_uncached`／`error`／`empty`／`valid` | 文件上次保存的计算结果／公式没有已保存的结果／单元格报错／空白／可读取 | 空白和没有已保存结果分别显示，保留原公式和单元格 |
| `commodity-research/src/tin/web/templates/sources.html:53、:82、:88、:96` | 直接显示来源、供应商核对结果和导入结果的完整 JSON | 数据来源和单位换算按数据日期、指标、原值、来源位置、记录数值、单位和换算方式展示。导入显示实际返回的条数和待核对旧值、新值 | 所缺字段不推断，保留零值。完整响应在管理员详情 |
| `commodity-research/src/tin/web/templates/sources.html:61、:102`；`commodity-research/src/tin/web/library.py:352` | 指标名后显示序列编号，文件名后显示数据集编号，要求提供内部编号 | 文件选项以文件名和导入时间识别，数据列选项展示工作表、指标名和单位。选择失效可重新选择文件版本和数据列 | 浏览器错误映射，原有 API 编号和校验保持兼容 |
| `commodity-research/src/tin/web/templates/sources.html:103` | 字段目录不完整，未采用截断结果／字段目录分页异常 | 指标列表没有读全，暂时无法确认选择。请重新选择文件并读取数据列 | 仍会拒绝不完整的列表 |
| `commodity-research/src/tin/web/library.py:480、:577、:610、:686`；`commodity-research/src/tin/provenance/router.py:123` | SHA256 不符、未关联错误文件、未返回后台任务号、原件已留存但未写入观测并附异常 | 版本无法核对时重新选择原件，处理未确认时先刷新记录，已保存原件但未导入时核对表格再导入 | 商品研究原件页和资料中心在界面映射。技术原响应及协议保留 |

### 商品研究｜日报、周报、旧报告图表与导出

| 文件:行号 | 改前 | 改后 | 说明 |
|---|---|---|---|
| `commodity-research/src/tin/web/templates/reports.html:17、:20`；`commodity-research/src/tin/web/templates/_report_judgment.html:13` | 依据当前已确认数据回放；并非历史冻结归档／可回放日期 | 用目前已确认的数据回看所选期间，研究判断可能晚于报告日期／查看当时保存的报告，保留生成时的数据和判断／选择要回看的日期 | 当前回看和旧报告分别注明时间口径 |
| `commodity-research/src/tin/web/templates/reports.html:21、:23、:29` | 准备绑定资料／WEEKLY RESEARCH／个人草稿 vN／03 证据图 | 请先在资料中心添加来源，再到“研究判断”保存本期观点／个人草稿 · 第 N 版／支撑本周或本日判断的图表 | 移除英文装饰标题，日期和版本保留 |
| `commodity-research/src/tin/web/templates/_report_saved_sources.html:2、:4、:6` | 未记录缺口，直接显示口径键、公式编号和状态 | 已检查的指标中暂未发现缺口，未纳入的指标未作检查。展开可读中文数据口径、计算指标名、单位、计算方法和状态 | 指标修订和真实来源仍可核对，未知计算方式标明未登记 |
| `commodity-research/src/tin/web/templates/_report_judgment.html:23`、`commodity-research/src/tin/web/templates/_report_saved_data.html:3`、`commodity-research/src/tin/web/templates/_report_strategy.html:28、:31`、`commodity-research/src/tin/web/static/report-figures.js:45`；`commodity-research/src/tin/web/report_period.py:20、:188` | 研判日期、带时区的登记时间及报告中的数据、策略事件时间直接显示 | 研判日期 YYYY-MM-DD · 系统登记于 YYYY-MM-DD HH:mm（北京时间）· 状态；数据日期、事件和图表时间按同一方式展示 | 在界面格式化，历史记录字段保持兼容 |
| `commodity-research/src/tin/web/templates/_report_judgment.html:22`、`commodity-research/src/tin/web/templates/_report_overview.html:15`、`commodity-research/src/tin/web/templates/_report_figures.html:15` | 尚未绑定可计算规则／暂无维度内容／历史冻结归档不重新加载图表 | 条件尚未开始自动监测，请到研究判断核对；本期可添加指标或写下变化；归档列出图表标题及引用位置 | 分别说明未监测、该栏暂无、归档图表所能查看的内容 |
| `commodity-research/src/tin/web/templates/reports.html:42` | 原始 JSON · 来源、输入与完整记录／下载 JSON | 报告使用的资料和数据缺口；管理员展开后可“下载报告诊断数据” | 普通研究员可展开来源口径，原有数据下载接口保持兼容 |
| `commodity-research/src/tin/web/static/report-drafts.js:64、:66、:87、:93` | 模型列表响应无效／模型编号／模型返回格式无效 | 暂时读不到可用的起草模型，可以先手写本期判断／未命名模型 N／这一栏没有生成可用草稿，可重新起草或手写，已有文字保留 | 原错误放管理员详情，已填写文本和逐栏核对继续保留 |
| `commodity-research/src/tin/web/static/report-drafts.js:123、:205、:220`；`commodity-research/src/tin/web/report_drafts.py:155` | vN／引用版本不一致，未替换为最新版本 | 《标题》· 第 N 版；所选引用版本或权限有变化时，请重新打开列表核对后再选择 | 不自动替换旧版本 |
| `commodity-research/src/tin/web/static/report-drafts.js:254–261`；`commodity-research/src/tin/web/report_drafts.py:283、:297、:350` | 请携带当前版本／来源版本无法核验／报告格式无效／保存响应不完整 | 版本过期先保留文字再打开最新版；引用变化时重新选择；无法确认保存时先复制文字，再查看最新版本 | 区别已知未保存和保存结果未确认，技术响应进管理员详情 |
| `commodity-research/src/tin/web/static/report-chart-import.js:32、:62、:204`；`commodity-research/src/tin/web/templates/chart_import.html:16` | 定义失败／修改映射或图型／无标题回退图表 ID／只导入图表结构并映射真实指标 | 暂不能生成时检查每条图例的对应指标和图表类型，再预览；缺标题显示原页码或所选序号；沿用旧图样式，用所选指标重新绘制 | 保留找不到指标、需要审核和可能重复等提示，原始失败原因进管理员详情 |
| `commodity-research/src/tin/web/static/report-ppt.js:28、:104`；`commodity-research/src/tin/web/report_ppt.py:122、:218、:259、:264` | 参数缺失、PNG 表单或失败记录与清单不符 | 未能按当前报告准备好全部图表，请重新打开导出窗口、核对选图、再导出 | 已生成后才列未能导出的图表名称。请求和校验协议保持兼容 |
| `commodity-research/src/tin/web/templates/restricted.html:20`、`commodity-research/src/tin/web/templates/indicators.html:25`、`commodity-research/src/tin/web/templates/_reports_readonly.html:1` | 主人归属信息尚未同步／更新后端后打开／每日快照与复盘归档 | 暂时无法确认共享范围，请联系研究台负责人核对／已分享的日报 | 权限不足或身份未核对仍不会展示资料 |

### 商品研究｜行情、研究维度、监测与图表

| 文件:行号 | 改前 | 改后 | 说明 |
|---|---|---|---|
| `commodity-research/src/tin/web/templates/_market_empty.html:5`、`commodity-research/src/tin/web/static/market-overview.js:187`；`commodity-research/src/tin/web/market.py:167` | 核心关注图表按需读取／无已确认观测／从真实字段生成建议／需要 kind:id | 向下查看已关注图表／还没有可展示指标，请添加来源并确认指标／从已导入指标挑选建议／请重新选择指标或图表 | 未知标题不再显示内部编号。生成流程不变 |
| `commodity-research/src/tin/web/static/dimensions.js:398、:429`；`commodity-research/src/tin/web/dimension_suggestions.py:228`、`commodity-research/src/tin/web/focus_suggestions.py:70、:107、:111` | 召回并核对真实指标／序列 ID／item_id／工作表 JSON | 正在为各研究环节查找可用指标；建议不能对应工作表或研究环节时提示重新生成或手动选择 | 保留原有目录和验证。常见错误通过浏览器映射 |
| `commodity-research/src/tin/web/templates/_judgment_manager.html:17、:56`；`commodity-research/src/tin/web/judgment_review.py:395`、`commodity-research/src/tin/web/judgment_structured.py:95` | 补标模型／导出 MD／未静默丢失模型候选／核对真实指标 ID | 为尚未分类的内容推荐维度／导出 Markdown 文档／本次最多核对 32 项，请拆分资料再分析／从列表重新选指标 | 保留容量限制和来源保护 |
| `commodity-research/src/tin/web/strategy_text.py:196、:200、:219`、`commodity-research/src/tin/web/monitors.py:350、:525` → `commodity-research/src/tin/web/static/app.js:28` | JSON 无效、固定列表、未保存调用审计、未采用固定规则、回测日期参数不一致 | 未生成可用建议时重试或手写；未能留存时说明尚未采用；重新选择完整回测日期 | 错误原文进入管理员详情。服务端原校验保持兼容 |
| `commodity-research/src/tin/web/static/monitors.js:168`、`commodity-research/src/tin/web/static/indicator-dedup.js:63、:80、:109`、`commodity-research/src/tin/web/templates/indicator.html:51` | 无名称时展示序列 ID、知识库 ID、角色引用 ID、原始时间和原始状态 | 显示有权限查看的库名、文件、工作表、指标和单位；历史显示动作与北京时间；指标详情展示中文引用类别 | 可用的供应商业务编码明确标识 |
| `commodity-research/src/tin/web/static/charts.js:193、:251`；`commodity-research/src/tin/web/charts.py:34、:469` | 修订 vN／原始修改时间、用户 ID、原始图表定义／来源版本2／当前 revision | 第 N 版 · 修改时间 · 修改人；请重新打开研究台更新授权或打开图表核对版本 | 管理员可展开原修订详情；保留可辨认的修改人名称并隐藏账号编号 |
| `commodity-research/src/tin/web/static/chart-ui.js:22、:24、:34`、`commodity-research/src/tin/web/static/favorites-client.js:87` | 未知原因回退原因码、显示校验原始字段、账号自选返回格式无效 | 不能绘制时请核对指标、单位、日期；图表设置有误时指出核对设置；自选列表请刷新后确认 | 已知原因如除数为零仍说明，原始错误放管理员详情 |

### 商品研究｜产业图与维护提示

| 文件:行号 | 改前 | 改后 | 说明 |
|---|---|---|---|
| `commodity-research/src/tin/web/static/diagram.js:93、:107、:108、:112` | HTTP 405、重启 uvicorn、实时取值不可用、方框不是真实状态 | 有保存版本时说明显示上次保存的数据，新加或改过的环节暂时无数值；两次失败时请刷新并说明当前状态不可用于判断 | 两次请求与现有回退逻辑仍保留。管理员可展开诊断 |
| `commodity-research/src/tin/web/templates/diagram.html:12、:231、:248、:278`、`commodity-research/src/tin/web/diagram_analysis_cache.py:91` | 从事实层现取／写回结构字段／派生指标公式 ID／模型或协议身份不完整 | 显示所选日期的指标；可导入环节和连接，未设置指标的可点开选择；无法确认设置时请重新选择模型 | Markdown 格式示例放在展开的语法说明中，导入规则保持兼容 |
| `commodity-research/src/tin/web/templates/needs_migration.html:14–21` | 数据库需要迁移，并直接显示服务目录和维护命令 | 研究工作台暂时无法打开。数据服务需要更新，请联系管理员，更新后刷新 | 未验证身份的独立运行页无管理员详情入口，原诊断 API 和运维日志仍可用 |

### 商品研究｜保留的技术资料

- 保留报告诊断下载、文件版本与 SHA256 校验、原件状态和库字段在原有 API 中的表示，避免影响调用方；本次调整正常页面及管理员详情展示。
- 产业图已展开的 Markdown 帮助保留实际导入语法，便于按现有文件格式编辑；主页面不再要求理解导入协议。
- 历史报告来源记录根据已保存数据展示。历史记录未保存单元格、文件版本或换算信息时，页面不补造依据。

## 简体和繁体共用的翻译文案

下列各页按翻译键列出简体文案的改前和改后，繁体包按同一键同步。现有键均保留。其他语言包仅为新增键补充默认文案，避免切换语言时出现键名。

### 资料和表格分析

| 文件:行号 | 改前 | 改后 | 说明 |
|---|---|---|---|
| `zh-CN.ts:3` | 缺少对应翻译 | 查看数据范围和计算依据 | `structuredReview.dataBasis`，繁体同步。 |
| `zh-CN.ts:4` | 缺少对应翻译 | 以下单元格仅供抽查：{cells}。 | `structuredReview.sampleCells`，繁体同步。 |
| `zh-CN.ts:5` | 缺少对应翻译 | 本次只读到部分数据，结果不能用于全表统计。 | `structuredReview.partialData`，繁体同步。 |
| `zh-CN.ts:6` | 缺少对应翻译 | 查看文件版本验证信息 | `structuredReview.versionDetails`，繁体同步。 |
| `zh-CN.ts:7` | 缺少对应翻译 | 登录已失效，请重新登录后继续。 | `structuredReview.sourceExpired`，繁体同步。 |
| `zh-CN.ts:8` | 缺少对应翻译 | 当前账号无权读取原件，请联系资料负责人。 | `structuredReview.sourceDenied`，繁体同步。 |
| `zh-CN.ts:9` | 缺少对应翻译 | 这份原件已不存在或暂时无法找到，请重新选择来源。 | `structuredReview.sourceMissing`，繁体同步。 |
| `zh-CN.ts:10` | 缺少对应翻译 | 正在读取原表，请稍后再查看其他区域。 | `structuredReview.sourceBusy`，繁体同步。 |
| `zh-CN.ts:11` | 缺少对应翻译 | 正在读取原表，请稍后再查看样本。 | `structuredReview.sampleBusy`，繁体同步。 |
| `zh-CN.ts:12` | 缺少对应翻译 | 原件版本已变化，请重新读取。 | `structuredReview.sourceChanged`，繁体同步。 |
| `zh-CN.ts:13` | 缺少对应翻译 | 原件暂时无法读取或来源已变化，请重新选择来源后再试。 | `structuredReview.sourceConflict`，繁体同步。 |
| `zh-CN.ts:14` | 缺少对应翻译 | 暂时无法读取原件，请稍后重试。 | `structuredReview.readFailed`，繁体同步。 |
| `zh-CN.ts:15` | 缺少对应翻译 | 当前 PDF 与回答时使用的版本不同，请重新选择原件。 | `structuredReview.pdfVersionChanged`，繁体同步。 |
| `zh-CN.ts:16` | 缺少对应翻译 | 找不到所选位置，请打开原件按表名核对。 | `structuredReview.pdfLocationMissing`，繁体同步。 |
| `zh-CN.ts:17` | 缺少对应翻译 | 没有记录这张表的页码，请从知识库打开 PDF 查找表名。 | `structuredReview.pdfPageUnknown`，繁体同步。 |
| `zh-CN.ts:18` | 缺少对应翻译 | 当前文件与回答时使用的版本不同，请重新选择原件。 | `structuredReview.fileVersionChanged`，繁体同步。 |
| `zh-CN.ts:19` | 缺少对应翻译 | 这次无法打开原表，请从资料列表重新选择文件。 | `structuredReview.openWorkbookFailed`，繁体同步。 |
| `zh-CN.ts:20` | 缺少对应翻译 | 没有记录确切的单元格位置，请打开原表核对。 | `structuredReview.noCellLocation`，繁体同步。 |
| `zh-CN.ts:21` | 缺少对应翻译 | 无法定位这个单元格，请打开原表核对。 | `structuredReview.cellLocationFailed`，繁体同步。 |
| `zh-CN.ts:22` | 缺少对应翻译 | 这次没有读到所选区域，请打开原表核对。 | `structuredReview.sheetRangeFailed`，繁体同步。 |
| `zh-CN.ts:23` | 缺少对应翻译 | 请先选择要查询的文件。 | `structuredReview.chooseFile`，繁体同步。 |
| `zh-CN.ts:24` | 缺少对应翻译 | 找不到要查询的工作表，请核对文件和工作表后重试。 | `structuredReview.chooseSheet`，繁体同步。 |
| `zh-CN.ts:25` | 缺少对应翻译 | 这次没有取得数值。请指定文件和工作表后再查，范围较大时可先查一张表。 | `structuredReview.fieldReadFailed`，繁体同步。 |
| `zh-CN.ts:26` | 缺少对应翻译 | 本次查询未完成，请重试。 | `structuredReview.fieldQueryFailed`，繁体同步。 |
| `zh-CN.ts:27` | 缺少对应翻译 | 本次查询未完成，请核对指标和查询范围后重试。 | `structuredReview.checkFieldRange`，繁体同步。 |
| `zh-CN.ts:28` | 缺少对应翻译 | 请选择你指的指标，确认后开始查询。 | `structuredReview.selectFieldHelp`，繁体同步。 |
| `zh-CN.ts:29` | 缺少对应翻译 | 请为每一项选择你指的指标，也可以在下方补充说明。 | `structuredReview.selectFieldHint`，繁体同步。 |
| `zh-CN.ts:30` | 缺少对应翻译 | 第 {index} 项指标 | `structuredReview.fieldOrdinal`，繁体同步。 |
| `zh-CN.ts:31` | 缺少对应翻译 | 需要确认的指标 | `structuredReview.fieldNeedsChoice`，繁体同步。 |
| `zh-CN.ts:32` | 缺少对应翻译 | 共 {count} 个来源列，以上列出其中一部分，请打开原表查看其余列。 | `structuredReview.partialHeaders`，繁体同步。 |
| `zh-CN.ts:33` | 缺少对应翻译 | 请对照来源核对公式、原单位和目标单位，再使用换算结果。 | `structuredReview.checkUnits`，繁体同步。 |
| `zh-CN.ts:67` | 缺少对应翻译 | 请用有权访问这个知识库的账号打开。 | `displayError.structuredAccess`，繁体同步。 |
| `zh-CN.ts:68` | 缺少对应翻译 | 表头自动识别尚未配置，请联系管理员。 | `displayError.structuredSetup`，繁体同步。 |
| `zh-CN.ts:69` | 缺少对应翻译 | 请从知识库重新打开这份表格。 | `displayError.structuredLibrary`，繁体同步。 |
| `zh-CN.ts:70` | 缺少对应翻译 | 请重新选择可用模型和要识别的表格范围。 | `displayError.structuredModel`，繁体同步。 |
| `zh-CN.ts:71` | 缺少对应翻译 | 本次最多可使用 128 份表格原件。请选择较少的来源后重试。 | `displayError.tooManyTableSources`，繁体同步。 |
| `zh-CN.ts:72` | 缺少对应翻译 | 无法打开导入时使用的文件版本，请重新选择原件。 | `displayError.sourceVersion`，繁体同步。 |
| `zh-CN.ts:73` | 缺少对应翻译 | 原件已保存，表格处理暂未确认。请先刷新处理记录。 | `displayError.processingUnconfirmed`，繁体同步。 |
| `zh-CN.ts:74` | 缺少对应翻译 | 请重新选择文件版本，再选择其中一列。 | `displayError.selectColumn`，繁体同步。 |

### 模型、搜索和资料同步设置

| 文件:行号 | 改前 | 改后 | 说明 |
|---|---|---|---|
| `zh-CN.ts:36` | 缺少对应翻译 | 请同时填写访问密钥编号和密钥。如果服务器已配置 AWS 访问权限，可将两项都留空。 | `displayError.storageCredentialsPair`，繁体同步。 |
| `zh-CN.ts:37` | 缺少对应翻译 | 未能连接文件存储，请核对存储空间名称、服务地址和访问权限后重试。 | `displayError.storageConnectionFailed`，繁体同步。 |
| `zh-CN.ts:41` | 缺少对应翻译 | token 是模型计量文字长度所用的单位，与字数并不一一对应。 | `displayError.modelLengthHelp`，繁体同步。 |
| `zh-CN.ts:42` | 缺少对应翻译 | 单次回复长度上限（token） | `displayError.replyLength`，繁体同步。 |
| `zh-CN.ts:43` | 缺少对应翻译 | 资料同步未完成，请检查资料来源和访问权限后重试。 | `displayError.syncFailed`，繁体同步。 |
| `zh-CN.ts:44` | 缺少对应翻译 | 用于连接文件解析服务，请填写服务商提供的密钥。 | `displayError.parserKeyHelp`，繁体同步。 |
| `zh-CN.ts:45` | 缺少对应翻译 | 服务密钥（API Key） | `displayError.parserKey`，繁体同步。 |
| `zh-CN.ts:46` | 缺少对应翻译 | 按服务商文档提供的名称选择模型版本。 | `displayError.parserVersionHelp`，繁体同步。 |
| `zh-CN.ts:47` | 缺少对应翻译 | 模型版本（Model Version） | `displayError.parserVersion`，繁体同步。 |
| `zh-CN.ts:48` | 缺少对应翻译 | 未能连接解析服务，请核对服务地址、密钥和所选模型后重新测试。 | `displayError.parserTestFailed`，繁体同步。 |
| `zh-CN.ts:75` | 缺少对应翻译 | 请核对存储空间名称（Bucket）和服务地址（Endpoint）。 | `displayError.readStorage`，繁体同步。 |
| `zh-CN.ts:76` | 缺少对应翻译 | 请填写服务地址、访问密钥编号和访问密钥。 | `displayError.storageCredentials`，繁体同步。 |
| `zh-CN.ts:77` | 缺少对应翻译 | 请检查服务器使用的 AWS 身份是否有权访问该存储空间。 | `displayError.storageRole`，繁体同步。 |
| `zh-CN.ts:128` | 相邻批次重叠 | 相邻两批重复读取的页数 | `pdfProcessing.overlap`，繁体同步。 |
| `zh-CN.ts:128` | 实际解析时限 | 每批实际处理最长时间 | `pdfProcessing.execution`，繁体同步。 |
| `zh-CN.ts:128` | 读取或保存设置失败，请重试。 | 暂时无法读取或保存 PDF 处理设置，请核对页数和时长后重试。 | `pdfProcessing.failed`，繁体同步。 |
| `zh-CN.ts:218` | 向量维度 | 向量维度（每条搜索记录中的数字个数） | `modelCatalog.dimension`，繁体同步。 |
| `zh-CN.ts:219` | 添加该向量模型时预填的维度，需与模型实际输出一致。 | 请按模型说明填写，数值应与模型实际输出的维度一致。 | `modelCatalog.dimensionDesc`，繁体同步。 |
| `zh-CN.ts:230` | 上下文窗口 | 单次可读取的内容上限 | `modelCatalog.context`，繁体同步。 |
| `zh-CN.ts:231` | 模型一次可处理的最大 token 数。 | 单位是 token，即模型计量文字长度所用的单位，与字数并不一一对应。 | `modelCatalog.contextDesc`，繁体同步。 |
| `zh-CN.ts:232` | 最大输出 | 单次回复长度上限 | `modelCatalog.output`，繁体同步。 |
| `zh-CN.ts:233` | 单次回复最多生成的 token 数。 | 单位是 token，即模型计量文字长度所用的单位，与字数并不一一对应。 | `modelCatalog.outputDesc`，繁体同步。 |
| `zh-CN.ts:780` | 邀请伙伴加入当前空间并分配角色。只有 Owner 可以新增或移除成员。 | 邀请同事加入空间并分配权限。只有空间所有者可以添加或移除成员。 | `tenantMember.sectionDescription`，繁体同步。 |
| `zh-CN.ts:781` | 了解 RBAC | 了解角色权限 | `tenantMember.learnRbacGuide`，繁体同步。 |
| `zh-CN.ts:836` | 角色必须是 owner / admin / contributor / viewer 之一。 | 请选择空间所有者、管理员、可编辑成员或只读成员。 | `tenantMember.errors.invalidRole`，繁体同步。 |
| `zh-CN.ts:875` | 每个角色在当前空间内能做的事情。服务端强制执行，UI 控件仅作展示。 | 查看每个角色能在当前空间做哪些操作。 | `tenantMember.permissions.desc`，繁体同步。 |
| `zh-CN.ts:1339` | 缺少对应翻译 | 同步暂未恢复，请重试。 | `datasource.resumeFailed`，繁体同步。 |
| `zh-CN.ts:1619` | 云盘文件夹 Token | 飞书云盘文件夹链接 | `datasource.drive.folderTokenLabel`，繁体同步。 |
| `zh-CN.ts:1620` | 输入 folder_token 或飞书云盘文件夹链接 | 请粘贴要同步的飞书文件夹链接 | `datasource.drive.folderTokenPlaceholder`，繁体同步。 |
| `zh-CN.ts:1621` | 请输入具体文件夹的 folder_token，不支持云空间根目录 | 请选择具体文件夹，工作台暂不支持同步整个云盘。 | `datasource.drive.folderTokenRequired`，繁体同步。 |
| `zh-CN.ts:1622` | 根目录不分页且不返回快捷方式，请选择具体文件夹 | 请选择要同步的文件夹，工作台暂不支持同步整个云盘。 | `datasource.drive.rootNotSupportedHint`，繁体同步。 |
| `zh-CN.ts:1626` | 在上方输入 folder_token（或从飞书云盘文件夹 URL 复制）并点击「加载」 | 在上方粘贴飞书文件夹链接，再点“加载”。 | `datasource.drive.placeholderDesc`，繁体同步。 |
| `zh-CN.ts:1629` | folder_token 不存在或已删除，请确认从飞书云盘文件夹 URL 复制的 token 正确。 | 找不到这个文件夹，可能已被删除。请在飞书重新打开文件夹，复制链接后再试。 | `datasource.drive.loadNotFoundHint`，繁体同步。 |
| `zh-CN.ts:1635` | 删除前查找文档失败，请查看服务器日志 | 暂时找不到要删除的文档，请刷新同步记录。 | `datasource.syncError.deletion_lookup_failed`，繁体同步。 |
| `zh-CN.ts:1636` | 删除失败，请查看服务器日志 | 暂时无法确认文档是否已删除，请刷新同步记录核对。 | `datasource.syncError.deletion_failed`，繁体同步。 |
| `zh-CN.ts:1637` | 导入失败，请查看服务器日志 | 资料未能导入，请核对文件和权限后重试同步。 | `datasource.syncError.ingest_failed`，繁体同步。 |
| `zh-CN.ts:1638` | 无权访问该 Seafile 文件，请检查令牌所属账号的资料库权限。 | 工作台没有访问该 Seafile 文件的权限，请联系资料负责人核对。 | `datasource.syncError.seafile_permission_denied`，繁体同步。 |
| `zh-CN.ts:1640` | Seafile 文件超过大小限制，请调大 MAX_FILE_SIZE_MB 或缩小该文件。 | 这个 Seafile 文件超过上传大小限制，请缩小文件后再同步。 | `datasource.syncError.seafile_file_too_large`，繁体同步。 |
| `zh-CN.ts:1643` | Seafile 返回了无法解析的响应，请检查服务器版本与反向代理配置。 | 暂时无法从 Seafile 读取文件资料，请联系管理员核对连接设置。 | `datasource.syncError.seafile_invalid_response`，繁体同步。 |
| `zh-CN.ts:1644` | Seafile 文件下载地址被 SSRF 策略拦截，请将 fileserver 域名加入 SSRF_WHITELIST。 | 工作台未获准访问该文件的下载地址，请联系管理员核对。 | `datasource.syncError.seafile_ssrf_blocked`，繁体同步。 |
| `zh-CN.ts:1737` | 启用后用户可在对话中上传图片并由 VLM 理解 | 开启后，可在对话中上传图片，让模型阅读图表和照片。 | `agentEditor.imageUpload.desc`，繁体同步。 |
| `zh-CN.ts:1738` | VLM 模型 | 图片理解模型（VLM） | `agentEditor.imageUpload.vlmModel`，繁体同步。 |
| `zh-CN.ts:1739` | 用于图片分析的视觉语言模型 | 用于阅读图片内容，包括图表和照片。 | `agentEditor.imageUpload.vlmModelDesc`，繁体同步。 |
| `zh-CN.ts:1740` | 请选择 VLM 模型 | 请选择用于阅读图片的模型 | `agentEditor.imageUpload.vlmModelPlaceholder`，繁体同步。 |
| `zh-CN.ts:1741` | 启用图片上传时必须选择 VLM 模型 | 请先选择用于阅读图片的模型，再开启图片上传。 | `agentEditor.imageUpload.vlmModelRequired`，繁体同步。 |
| `zh-CN.ts:1742` | 附件图片理解 / 扫描件 OCR | 阅读附件图片和扫描件文字 | `agentEditor.imageUpload.imageUnderstandingLabel`，繁体同步。 |
| `zh-CN.ts:1743` | 对图片型 PDF/PPT 等扫描件，当解析不出文字时用 VLM 进行 OCR 识别。开启会增加解析耗时，默认关闭。 | 扫描件中的文字无法直接选取时，可开启文字识别（OCR）来读取。开启后会增加处理时间。 | `agentEditor.imageUpload.imageUnderstandingDesc`，繁体同步。 |
| `zh-CN.ts:1744` | 扫描件 OCR 最大页数 | 每份扫描件最多识别的页数 | `agentEditor.imageUpload.ocrMaxPagesLabel`，繁体同步。 |
| `zh-CN.ts:1745` | 扫描件送入 VLM 做 OCR 的最大页数，越多覆盖越全但越慢、越贵。0 表示使用全局默认。 | 处理页数越多，等待时间和费用可能越高。填 0 时使用空间默认值。 | `agentEditor.imageUpload.ocrMaxPagesDesc`，繁体同步。 |
| `zh-CN.ts:2805` | 向量存储 | 知识库搜索数据存储 | `kbSettings.vectorStore.title`，繁体同步。 |
| `zh-CN.ts:2806` | 选择此知识库要写入的向量存储。绑定不可更改 — 如需将现有 KB 迁移到其他存储，请创建新 KB 并重新索引。 | 选择知识库搜索数据的保存位置。创建后不能更换；如需迁移，请新建知识库并重新生成搜索数据。 | `kbSettings.vectorStore.description`，繁体同步。 |
| `zh-CN.ts:2807` | 正在加载向量存储列表... | 正在读取可用的存储位置… | `kbSettings.vectorStore.loading`，繁体同步。 |
| `zh-CN.ts:2808` | 向量存储 | 保存位置 | `kbSettings.vectorStore.engineLabel`，繁体同步。 |
| `zh-CN.ts:2809` | 从全局向量存储配置中选择，或保持系统默认以使用空间的 RETRIEVE_DRIVER 引擎。 | 选择管理员添加的存储位置，或使用空间默认设置。 | `kbSettings.vectorStore.engineDesc`，繁体同步。 |
| `zh-CN.ts:2810` | 已绑定的向量存储 | 当前保存位置 | `kbSettings.vectorStore.boundLabel`，繁体同步。 |
| `zh-CN.ts:2811` | 系统默认 | 空间默认 | `kbSettings.vectorStore.systemDefault`，繁体同步。 |
| `zh-CN.ts:2812` | 创建后不可更改。如需迁移，请创建一个绑定到目标存储的新 KB 并重新索引。 | 创建后不能更换存储位置。如需迁移，请创建新知识库并重新生成搜索数据。 | `kbSettings.vectorStore.immutableHint`，繁体同步。 |
| `zh-CN.ts:2813` | 向量存储绑定在创建后无法更改。 | 知识库创建后，搜索数据的保存位置不能修改。 | `kbSettings.vectorStore.immutableEdit`，繁体同步。 |
| `zh-CN.ts:3764` | 根据已同步且启用的 Tools 生成精简说明，填入后可编辑，保存后生效。 | 根据已启用的外部工具清单生成说明。你可以修改，保存后生效。 | `mcpMetadata.generateHint`，繁体同步。 |
| `zh-CN.ts:3767` | 生成失败，请确认 Tools 已同步且已配置可用的对话模型后重试。 | 生成未完成，请先同步外部服务的工具清单，并确认有可用的对话模型。 | `mcpMetadata.generateFailed`，繁体同步。 |
| `zh-CN.ts:3996` | Embedding 模型必须填写有效的向量维度（128-4096） | 请按模型说明填写向量维度，也就是每条搜索记录含有的数字个数。可填 128 至 4096。 | `modelSettings.toasts.dimensionInvalid`，繁体同步。 |
| `zh-CN.ts:4020` | ReRank 模型 | 结果排序模型（Rerank） | `modelSettings.rerank.title`，繁体同步。 |
| `zh-CN.ts:4021` | 配置用于结果重排序的模型 | 从初步找到的资料中挑出更相关的结果并重新排列。 | `modelSettings.rerank.desc`，繁体同步。 |
| `zh-CN.ts:4022` | 暂无 ReRank 模型 | 还没有可用的结果排序模型 | `modelSettings.rerank.empty`，繁体同步。 |
| `zh-CN.ts:4025` | Embedding 模型 | 语义搜索模型（Embedding） | `modelSettings.embedding.title`，繁体同步。 |
| `zh-CN.ts:4026` | 配置用于文本向量化的嵌入模型 | 用于查找和问题意思相近的资料。 | `modelSettings.embedding.desc`，繁体同步。 |
| `zh-CN.ts:4027` | 暂无 Embedding 模型 | 还没有可用的语义搜索模型 | `modelSettings.embedding.empty`，繁体同步。 |
| `zh-CN.ts:4809` | 移除 SSRF 白名单条目 | 移除已允许访问的地址 | `system.globalSettings.listConfirm.ssrf.whitelist.remove.header`，繁体同步。 |
| `zh-CN.ts:4810` | 确认从 SSRF 白名单中移除 {entry}？移除后该条目将重新受 SSRF 防护拦截。 | 确认移除 {entry}？移除后，工作台将重新按网络访问保护规则检查该地址。 | `system.globalSettings.listConfirm.ssrf.whitelist.remove.body`，繁体同步。 |
| `zh-CN.ts:4814` | 添加 SSRF 白名单条目 | 允许工作台访问这个地址 | `system.globalSettings.listConfirm.ssrf.whitelist.add.header`，繁体同步。 |
| `zh-CN.ts:4815` | 确认把 {entry} 加入 SSRF 白名单？该条目匹配到的主机 / IP / 网段会绕过 SSRF 防护，可能让 Agent 访问内网服务，请仅在确知用途时添加。 | 允许访问 {entry} 后，助手可能访问该地址上的内部服务。请核对地址和用途再确认。 | `system.globalSettings.listConfirm.ssrf.whitelist.add.body`，繁体同步。 |
| `zh-CN.ts:4894` | SSRF 防护白名单 | 允许访问的网络地址（SSRF 例外） | `system.globalSettings.keyLabels.ssrf.whitelist`，繁体同步。 |
| `zh-CN.ts:5560` | 正在读取流水线… | 正在读取图片处理方式… | `knowledgeEditor.advanced.multimodal.imagePipelineLoading`，繁体同步。 |
| `zh-CN.ts:5561` | 读取流水线失败 | 暂时无法读取图片处理方式，请重试。 | `knowledgeEditor.advanced.multimodal.imagePipelineLoadError`，繁体同步。 |
| `zh-CN.ts:5562` | 无法校验当前设置，保存时后端会再次检查。 | 保存时会再次检查所选方式是否可用。 | `knowledgeEditor.advanced.multimodal.imagePipelineValidateError`，繁体同步。 |
| `zh-CN.ts:5620` | 分块设置 | 正文分段方式 | `knowledgeEditor.chunking.title`，繁体同步。 |
| `zh-CN.ts:5621` | 控制上传文档在嵌入前的切分方式。默认值适用于大多数场景，仅在检索质量异常时调整。 | 设置上传后如何整理正文段落。默认设置适合大多数文档，搜索效果不理想时再调整。 | `knowledgeEditor.chunking.description`，繁体同步。 |
| `zh-CN.ts:5622` | 分块大小 | 每段最多字符数 | `knowledgeEditor.chunking.sizeLabel`，繁体同步。 |
| `zh-CN.ts:5623` | 每个分块的最大字符数（100-4000）。默认 512 ≈ 中文 300 tokens / 英文 100-130 tokens。FAQ 用 200-400，叙述性长文档用 1000-2000。 | 可填 100 至 4000。简短问答可用较短段落，完整叙述可用较长段落。 | `knowledgeEditor.chunking.sizeDescription`，繁体同步。 |
| `zh-CN.ts:5625` | 分块重叠 | 相邻两段重复保留的字符数 | `knowledgeEditor.chunking.overlapLabel`，繁体同步。 |
| `zh-CN.ts:5626` | 相邻分块之间共享的字符数（0-500）。默认 80 ≈ 分块大小的 15%，符合当前研究推荐。FAQ/结构化数据用 0，长篇叙述用 150-200。 | 可填 0 至 500。保留少量相邻文字可以让跨段的内容更完整。 | `knowledgeEditor.chunking.overlapDescription`，繁体同步。 |
| `zh-CN.ts:5630` | 父子分块 | 按短段查找并带上前后文 | `knowledgeEditor.chunking.parentChildLabel`，繁体同步。 |
| `zh-CN.ts:5631` | 两级分块：小的子块用于向量匹配（精准命中），大的父块返回给 LLM（更丰富上下文）。建议用于长文档（>10 页）；短 FAQ 可关闭以节省存储。 | 先按短段找到相关内容，再带上较长的上下文供回答参考。适合长文档。 | `knowledgeEditor.chunking.parentChildDescription`，繁体同步。 |
| `zh-CN.ts:5632` | 父块大小 | 前后文的字符数 | `knowledgeEditor.chunking.parentChunkSizeLabel`，繁体同步。 |
| `zh-CN.ts:5633` | 返回给 LLM 的上下文块大小（512-8192）。默认 4096 ≈ 1000 英文 tokens，适合所有现代 LLM 上下文窗口。 | 找到相关段落后，提供给助手参考的上下文长度。可填 512 至 8192。 | `knowledgeEditor.chunking.parentChunkSizeDescription`，繁体同步。 |
| `zh-CN.ts:5634` | 子块大小 | 用于查找的每段字符数 | `knowledgeEditor.chunking.childChunkSizeLabel`，繁体同步。 |
| `zh-CN.ts:5635` | 用于向量匹配的嵌入块大小（64-2048）。默认 384 ≈ 80 tokens，是 sentence-transformer / BGE 类嵌入模型的最佳点。 | 用来匹配问题的短段长度。可填 64 至 2048。 | `knowledgeEditor.chunking.childChunkSizeDescription`，繁体同步。 |
| `zh-CN.ts:5636` | 分块策略 | 怎样划分段落 | `knowledgeEditor.chunking.strategyLabel`，繁体同步。 |
| `zh-CN.ts:5637` | 选择文档的分块方式。自动模式会分析每个文档的结构并选择最佳策略。 | 可选择分段方式。自动模式会根据文档结构选择适用的方式。 | `knowledgeEditor.chunking.strategyDescription`，繁体同步。 |
| `zh-CN.ts:5638` | 选择分块策略（不填则按长度切分） | 选择分段方式，留空时按字符数划分 | `knowledgeEditor.chunking.strategyPlaceholder`，繁体同步。 |
| `zh-CN.ts:5639` | 重叠相对于分块大小较大——分块之间会共享大部分内容。 | 相邻段落会保留较多重复内容，可缩短重复长度。 | `knowledgeEditor.chunking.overlapWarning`，繁体同步。 |
| `zh-CN.ts:5641` | 每块 Token 上限 | 每段的模型长度上限（token） | `knowledgeEditor.chunking.tokenLimitLabel`，繁体同步。 |
| `zh-CN.ts:5642` | 每个分块的硬性 Token 上限（0-8192）。0 = 关闭（仅按字符数）。当嵌入模型 Token 上限较小时启用：MiniLM (256 tok) 用 200，BGE/Cohere (512 tok) 用 400。现代嵌入器（OpenAI、Voyage、Jina-v3）支持 >2000 tokens，保持 0 即可。 | token 是模型计量文字长度的单位，与字数并不一一对应。可填 0 至 8192，填 0 时只按字符数限制。 | `knowledgeEditor.chunking.tokenLimitDescription`，繁体同步。 |
| `zh-CN.ts:5658` | 策略链已穷尽 — 当前设置无法智能分块此内容 | 当前方式无法整理这段文字，请更换分段方式后再预览。 | `knowledgeEditor.chunking.debug.fallbackWarning`，繁体同步。 |
| `zh-CN.ts:5760` | 仅索引问题可提升精度，索引问答可提高召回率 | 搜索时可只匹配问题，也可以同时匹配问题和答案。 | `knowledgeEditor.faq.indexModeDescription`，繁体同步。 |
| `zh-CN.ts:5761` | 问题索引方式 | 如何查找相似问法 | `knowledgeEditor.faq.questionIndexModeLabel`，繁体同步。 |
| `zh-CN.ts:5762` | 合并索引：标准问和相似问合并索引；分别索引：标准问和每个相似问独立索引，检索更精确但需要更多存储 | 把相似问法合并查找，或分别查找每种问法。分别查找需要更多存储空间。 | `knowledgeEditor.faq.questionIndexModeDescription`，繁体同步。 |
| `zh-CN.ts:5826` | 支持点击或拖拽上传，多格式文档自动解析并智能分块，快速构建可检索的知识库 | 点击或拖入文件。上传处理完成后即可搜索文档正文。 | `knowledgeEditor.document.subtitle`，繁体同步。 |
| `zh-CN.ts:5890` | Slug（页面地址） | 页面地址后缀 | `knowledgeEditor.wikiBrowser.newPageSlugLabel`，繁体同步。 |
| `zh-CN.ts:5895` | 请填写标题和 Slug | 请填写标题和页面地址。 | `knowledgeEditor.wikiBrowser.newPageMissingFields`，繁体同步。 |
| `zh-CN.ts:6016` | Wiki 页面之间的引用关系图（即页面链接图谱），与「知识库设置 → 知识图谱」中基于 LLM 抽取的实体-关系图谱不是同一个概念 | 查看本页引用的页面，以及引用本页的其他页面。 | `knowledgeEditor.wikiBrowser.tabGraphTip`，繁体同步。 |
| `zh-CN.ts:6106` | 该节点没有链接邻居 | 此页尚无关联页面。 | `knowledgeEditor.wikiBrowser.neighborsNone`，繁体同步。 |
| `zh-CN.ts:6131` | 索引策略 | 上传后如何整理和搜索文档 | `knowledgeEditor.indexing.title`，繁体同步。 |
| `zh-CN.ts:6132` | 配置文档上传后的处理管道，可以独立开启或关闭每种索引方式 | 选择文档上传后要提供哪些搜索和浏览方式。 | `knowledgeEditor.indexing.description`，繁体同步。 |
| `zh-CN.ts:6133` | RAG 检索 | 按意思和关键词搜索 | `knowledgeEditor.indexing.searchTitle`，繁体同步。 |
| `zh-CN.ts:6134` | 对文档进行分块、向量化和关键词索引，支持混合检索 | 整理文档正文，让用户按意思和关键词查找相关段落。 | `knowledgeEditor.indexing.searchDesc`，繁体同步。 |
| `zh-CN.ts:6139` | 至少需要开启一种索引策略 | 请至少选择一种整理或搜索方式。 | `knowledgeEditor.indexing.atLeastOne`，繁体同步。 |
| `zh-CN.ts:6140` | RAG 检索需要配置 Embedding 模型 | 请先配置用来查找意思相近资料的模型（Embedding）。 | `knowledgeEditor.indexing.embeddingRequired`，繁体同步。 |
| `zh-CN.ts:6141` | 知识库已有内容，索引策略暂不支持调整。如需变更，请先清空知识库。 | 此知识库已有内容，当前不能更改搜索方式。请联系管理员评估更换方式对已有资料的影响。 | `knowledgeEditor.indexing.lockedTip`，繁体同步。 |
| `zh-CN.ts:6563` | 创建会话失败 | 暂时无法开始新对话，请重试。 | `createChat.messages.createFailed`，繁体同步。 |
| `zh-CN.ts:6622` | 会话 ID 不存在 | 找不到这个对话，请从对话列表重新打开。 | `input.messages.sessionMissing`，繁体同步。 |
| `zh-CN.ts:6623` | 无法获取消息 ID，请刷新页面后重试 | 找不到这条消息，请重新打开对话。 | `input.messages.messageMissing`，繁体同步。 |
| `zh-CN.ts:6944` | 配置实体-关系提取功能，自动从文本中抽取实体和关系构建知识图谱（注意：这与 Wiki 知识库中的「页面链接图谱」是两回事——前者是基于 LLM 的实体-关系图，后者是 Wiki 页面之间的引用关系图） | 从正文中识别机构、商品等对象及其关系，让用户可以查看它们之间的联系。 | `graphSettings.description`，繁体同步。 |
| `zh-CN.ts:6995` | 召回 | 先查找资料 | `retrievalSettings.recallTab`，繁体同步。 |
| `zh-CN.ts:6996` | 重排序 | 比较并筛选结果 | `retrievalSettings.rerankTab`，繁体同步。 |
| `zh-CN.ts:6997` | 结果融合（RRF） | 合并两种查找结果（RRF） | `retrievalSettings.rrfTitle`，繁体同步。 |
| `zh-CN.ts:6998` | RRF 平滑常数 (K) | 合并时对靠前结果的调整（K） | `retrievalSettings.rrfKLabel`，繁体同步。 |
| `zh-CN.ts:6999` | 值越大，排名靠前结果的优势越小。默认 60；设为 0 使用默认值。 | 合并按意思和按关键词找到的资料。K 越大，单项结果排在前面的优势越小。填 0 使用默认值 60。 | `retrievalSettings.rrfKDescription`，繁体同步。 |
| `zh-CN.ts:7000` | RRF 向量权重 | 意思相近的结果占多大比重 | `retrievalSettings.rrfVectorWeightLabel`，繁体同步。 |
| `zh-CN.ts:7001` | 控制向量检索对融合结果的影响。默认 0.7；设为 0 使用默认值。两项权重之和无需等于 1。 | 数值越大，意思相近的资料对合并后顺序的影响越大。填 0 使用默认值 0.7。两项比重之和无需等于 1。 | `retrievalSettings.rrfVectorWeightDescription`，繁体同步。 |
| `zh-CN.ts:7002` | RRF 关键词权重 | 关键词匹配的结果占多大比重 | `retrievalSettings.rrfKeywordWeightLabel`，繁体同步。 |
| `zh-CN.ts:7003` | 控制关键词检索对融合结果的影响。默认 0.3；设为 0 使用默认值。两项权重之和无需等于 1。 | 数值越大，关键词匹配的资料对合并后顺序的影响越大。填 0 使用默认值 0.3。两项比重之和无需等于 1。 | `retrievalSettings.rrfKeywordWeightDescription`，繁体同步。 |
| `zh-CN.ts:7005` | 配置知识库搜索和消息搜索的全局检索参数 | 设置知识库和对话记录如何搜索。 | `retrievalSettings.description`，繁体同步。 |
| `zh-CN.ts:7006` | 向量检索数量 (Top K) | 先找多少条意思相近的资料（Top K） | `retrievalSettings.embeddingTopKLabel`，繁体同步。 |
| `zh-CN.ts:7007` | 向量相似度阈值 | 按意思查找时的最低匹配程度 | `retrievalSettings.vectorThresholdLabel`，繁体同步。 |
| `zh-CN.ts:7008` | 关键词匹配阈值 | 按关键词查找时的最低匹配程度 | `retrievalSettings.keywordThresholdLabel`，繁体同步。 |
| `zh-CN.ts:7009` | Rerank 数量 (Top K) | 重新比较后保留几条（Top K） | `retrievalSettings.rerankTopKLabel`，繁体同步。 |
| `zh-CN.ts:7010` | Rerank 阈值 | 供回答参考所需的最低相关程度 | `retrievalSettings.rerankThresholdLabel`，繁体同步。 |
| `zh-CN.ts:7011` | Rerank 模型 | 结果排序模型（Rerank） | `retrievalSettings.rerankModelLabel`，繁体同步。 |
| `zh-CN.ts:7012` | 选择用于搜索结果重排序的模型 | 从初步找到的资料中挑出更相关的结果并重新排列。 | `retrievalSettings.rerankModelDescription`，繁体同步。 |
| `zh-CN.ts:7013` | 请选择 Rerank 模型，搜索功能需要此模型对结果进行重排序 | 请先选择用于比较和排列搜索结果的模型。 | `retrievalSettings.rerankModelRequired`，繁体同步。 |
| `zh-CN.ts:7016` | 保存配置失败: {message} | 搜索设置未能保存，请保留当前填写内容后重试。 | `retrievalSettings.toasts.saveFailed`，繁体同步。 |
| `zh-CN.ts:7161` | 环境变量 | 供我的工具使用的密钥 | `envVarSettings.host.title`，繁体同步。 |
| `zh-CN.ts:7162` | 给本机技能用的个人密钥，不是工作台的系统或部署配置。 | 这些密钥供这台电脑上的个人工具使用。 | `envVarSettings.host.description`，繁体同步。 |
| `zh-CN.ts:7163` | 环境变量说明 | 个人工具密钥说明 | `envVarSettings.host.helpAria`，繁体同步。 |
| `zh-CN.ts:7164` | 技能在这台电脑上运行时才会注入；对话里也可以当场提供。保存后不再显示明文。 | 工具运行时才能使用密钥。保存后，页面不再显示密钥原文。 | `envVarSettings.host.introRuntimeBody`，繁体同步。 |
| `zh-CN.ts:7171` | 给技能和沙箱用的个人密钥，不是工作台的系统或部署配置。 | 这些密钥供你自己的工具和命令使用。 | `envVarSettings.description`，繁体同步。 |
| `zh-CN.ts:7174` | 只注入到你自己的对话和执行里，空间里的其他人看不到，也不会改成他们的值。 | 只有你运行的工具和命令可以使用，空间里的其他人无法查看。 | `envVarSettings.introPersonalBody`，繁体同步。 |
| `zh-CN.ts:7176` | 技能运行或在沙箱里执行命令时才会注入；对话里也可以当场提供。保存后不再显示明文。 | 工具运行时才能使用。保存后，页面不再显示密钥原文。 | `envVarSettings.introRuntimeBody`，繁体同步。 |
| `zh-CN.ts:7180` | 还没有沙箱 | 尚未启用工具运行环境 | `envVarSettings.noConfigTitle`，繁体同步。 |
| `zh-CN.ts:7181` | 这个空间还没有配置沙箱后端，暂时没有可设置的密钥。 | 此空间尚未启用工具运行环境，请联系管理员。 | `envVarSettings.noConfigDescription`，繁体同步。 |
| `zh-CN.ts:7243` | 默认 Embedding 模型 | 默认语义搜索模型（Embedding） | `defaultSettings.slots.embedding.label`，繁体同步。 |
| `zh-CN.ts:7244` | 新建知识库时默认选用。已有知识库不受影响。 | 新知识库会默认用它查找意思相近的资料，已创建的知识库继续使用原有设置。 | `defaultSettings.slots.embedding.description`，繁体同步。 |
| `zh-CN.ts:7245` | 空间内没有可用的 Embedding 模型，无法新建知识库。 | 空间内没有可用的语义搜索模型。请先配置模型，再创建知识库。 | `defaultSettings.slots.embedding.none`，繁体同步。 |
| `zh-CN.ts:7248` | 默认 Rerank 模型 | 默认结果排序模型（Rerank） | `defaultSettings.slots.rerank.label`，繁体同步。 |
| `zh-CN.ts:7249` | 未单独配置重排模型的检索和内置智能体使用。 | 用于重新比较并排列搜索结果。没有单独设置时会使用此模型。 | `defaultSettings.slots.rerank.description`，繁体同步。 |
| `zh-CN.ts:7250` | 空间内没有可用的 Rerank 模型，检索将按召回顺序返回。 | 空间内没有可用的结果排序模型，搜索会沿用初步找到资料的顺序。 | `defaultSettings.slots.rerank.none`，繁体同步。 |
| `zh-CN.ts:7321` | 配置聊天历史知识库，将对话消息自动向量化索引，实现语义搜索 | 设置对话记录能否按意思搜索。 | `chatHistorySettings.description`，繁体同步。 |
| `zh-CN.ts:7322` | 启用消息索引 | 允许按意思搜索对话 | `chatHistorySettings.enableLabel`，繁体同步。 |
| `zh-CN.ts:7323` | 开启后，新的对话消息将自动索引到知识库，支持向量搜索 | 开启后，可以按意思搜索新的对话内容。 | `chatHistorySettings.enableDescription`，繁体同步。 |
| `zh-CN.ts:7324` | Embedding 模型 | 语义搜索模型（Embedding） | `chatHistorySettings.embeddingModelLabel`，繁体同步。 |
| `zh-CN.ts:7325` | 选择用于消息向量化的 Embedding 模型 | 用来查找意思相近的对话内容。 | `chatHistorySettings.embeddingModelDescription`，繁体同步。 |
| `zh-CN.ts:7326` | 已有消息被索引，Embedding 模型不可修改（修改需清空索引数据） | 已有聊天搜索数据时，更换模型需要先清除并重建这些搜索数据。 | `chatHistorySettings.embeddingModelLocked`，繁体同步。 |
| `zh-CN.ts:7327` | 索引统计 | 对话搜索统计 | `chatHistorySettings.statsTitle`，繁体同步。 |
| `zh-CN.ts:7328` | 已索引消息 | 已可搜索的消息 | `chatHistorySettings.statsIndexedMessages`，繁体同步。 |
| `zh-CN.ts:7329` | 消息索引未配置 | 尚未开启对话搜索 | `chatHistorySettings.statsNotConfigured`，繁体同步。 |
| `zh-CN.ts:7330` | 启用并选择 Embedding 模型后，对话消息将自动向量化索引 | 开启并选择语义搜索模型后，新对话可以按意思搜索。 | `chatHistorySettings.statsNotConfiguredDesc`，繁体同步。 |
| `zh-CN.ts:7333` | 保存配置失败: {message} | 对话搜索设置未能保存，请保留当前填写内容后重试。 | `chatHistorySettings.toasts.saveFailed`，繁体同步。 |
| `zh-CN.ts:7337` | 向量数据库引擎 | 知识库搜索数据存储 | `vectorStoreSettings.title`，繁体同步。 |
| `zh-CN.ts:7338` | 注册和管理用于知识库搜索的向量数据库实例。 | 管理用来保存知识库搜索数据的数据库。 | `vectorStoreSettings.description`，繁体同步。 |
| `zh-CN.ts:7350` | DEFAULT | 空间默认 | `vectorStoreSettings.envTag`，繁体同步。 |
| `zh-CN.ts:7360` | 连接测试失败 | 未能连到所选服务，请核对地址和访问凭据后重新测试。 | `vectorStoreSettings.toasts.testFailed`，繁体同步。 |
| `zh-CN.ts:7362` | 发生错误，请重试。 | 暂时无法确认存储设置是否已更新，请刷新后核对。 | `vectorStoreSettings.toasts.errorGeneric`，繁体同步。 |
| `zh-CN.ts:8153` | Embedding 模型连接测试失败，无法获取向量维度 | 未能连接语义搜索模型。请核对服务地址、密钥和所选模型，再重新测试。 | `settings.weknoraCloud.addModelsEmbeddingFailed`，繁体同步。 |

### 登录与邀请

| 文件:行号 | 改前 | 改后 | 说明 |
|---|---|---|---|
| `zh-CN.ts:38` | 缺少对应翻译 | 这个用户名已被使用，请换一个名字再注册。 | `displayError.usernameRegistered`，繁体同步。 |
| `zh-CN.ts:60` | 缺少对应翻译 | 请填写邮箱和密码后登录。 | `displayError.loginFields`，繁体同步。 |
| `zh-CN.ts:61` | 缺少对应翻译 | 注册链接不完整，请从邀请邮件重新打开。 | `displayError.invitationIncomplete`，繁体同步。 |
| `zh-CN.ts:62` | 缺少对应翻译 | 请核对邮箱、用户名和密码后再提交。 | `displayError.registerFields`，繁体同步。 |
| `zh-CN.ts:63` | 缺少对应翻译 | 此空间没有开放注册，请联系管理员获取邀请。 | `displayError.registerDisabled`，繁体同步。 |
| `zh-CN.ts:64` | 缺少对应翻译 | 这个邮箱已有账号，请登录后继续。 | `displayError.emailRegistered`，繁体同步。 |
| `zh-CN.ts:4522` | 用户名或密码错误 | 邮箱或密码不正确，请核对后再试。 | `error.invalidCredentials`，繁体同步。 |
| `zh-CN.ts:4523` | Token刷新失败 | 暂时无法确认登录状态，请重试。 | `error.tokenRefreshFailed`，繁体同步。 |
| `zh-CN.ts:4524` | 请重新登录 | 登录已失效，请重新登录后继续。 | `error.pleaseRelogin`，繁体同步。 |
| `zh-CN.ts:4530` | 未找到登录令牌，请重新登录 | 登录已失效，请重新登录后继续。 | `error.tokenNotFound`，繁体同步。 |
| `zh-CN.ts:6849` | 使用 OIDC 登录 | 使用单位账号登录 | `auth.oidcLogin`，繁体同步。 |
| `zh-CN.ts:6851` | 正在跳转到身份提供商... | 正在打开单位登录页面… | `auth.redirectingToOIDC`，繁体同步。 |
| `zh-CN.ts:6853` | OIDC 登录失败 | 暂时无法完成单位账号登录，请重试。如果仍无法登录，请联系空间管理员。 | `auth.oidcLoginFailed`，繁体同步。 |
| `zh-CN.ts:6869` | 注册失败 | 暂时无法确认注册结果。请先尝试登录，仍无法进入时再重新注册。 | `auth.registerFailed`，繁体同步。 |
| `zh-CN.ts:6892` | 登录错误，请检查邮箱或密码 | 邮箱或密码不正确，请核对后再试。 | `auth.loginError`，繁体同步。 |
| `zh-CN.ts:6893` | 登录错误，请稍后重试 | 暂时无法登录，请稍后重试。 | `auth.loginErrorRetry`，繁体同步。 |
| `zh-CN.ts:6894` | 注册错误，请稍后重试 | 暂时无法确认注册结果。请先尝试登录，仍无法进入时再重新注册。 | `auth.registerError`，繁体同步。 |

### 定时任务、生成文件、工具和系统管理

| 文件:行号 | 改前 | 改后 | 说明 |
|---|---|---|---|
| `zh-CN.ts:39` | 缺少对应翻译 | 任务未能完成，请查看受影响的资料后重试。 | `displayError.runtimeTaskFailed`，繁体同步。 |
| `zh-CN.ts:49` | 缺少对应翻译 | 本次定时任务未完成。请查看任务输入后再运行。 | `displayError.automationFailed`，繁体同步。 |
| `zh-CN.ts:50` | 缺少对应翻译 | 未命名的任务操作 | `displayError.automationStep`，繁体同步。 |
| `zh-CN.ts:51` | 缺少对应翻译 | 机器人编号（Bot ID） | `displayError.botId`，繁体同步。 |
| `zh-CN.ts:52` | 缺少对应翻译 | 机器人密钥（Bot Secret） | `displayError.botSecret`，繁体同步。 |
| `zh-CN.ts:53` | 缺少对应翻译 | 长连接地址（WebSocket Endpoint） | `displayError.botEndpoint`，繁体同步。 |
| `zh-CN.ts:54` | 缺少对应翻译 | 消息验证令牌（Verification Token） | `displayError.botVerification`，繁体同步。 |
| `zh-CN.ts:55` | 缺少对应翻译 | 消息加密密钥（Encrypt Key） | `displayError.botEncryption`，繁体同步。 |
| `zh-CN.ts:56` | 缺少对应翻译 | 出站消息验证令牌（Outgoing Webhook Token） | `displayError.botOutbound`，繁体同步。 |
| `zh-CN.ts:336` | Cron 表达式 | Cron 定时规则 | `automation.editor.cron`，繁体同步。 |
| `zh-CN.ts:390` | 自定义 cron | 自定义运行时间 | `automation.schedule.kinds.custom`，繁体同步。 |
| `zh-CN.ts:395` | 五段式：分 时 日 月 周，例如 0 9 * * 1-5 表示工作日 9:00。最短间隔 5 分钟。 | 按分、时、日、月、星期填写。例如 0 9 * * 1-5 表示工作日 9:00。两次运行至少间隔 5 分钟，下方会预览运行时间。 | `automation.schedule.customHint`，繁体同步。 |
| `zh-CN.ts:405` | cron：{expr} | 按自定义时间运行 | `automation.schedule.describe.custom`，繁体同步。 |
| `zh-CN.ts:4849` | 数据源同步、批处理和清理任务的每实例并发，与用户面流水线硬隔离；修改后需重启。 | 每个服务实例可以同时执行的维护任务数，例如同步、批处理和清理。与日常处理分别计数，修改后需重启服务。 | `system.globalSettings.keyDescriptions.asynq.maintenance_concurrency`，繁体同步。 |
| `zh-CN.ts:5329` | 沙箱可视化 | 临时工作桌面 | `chat.sandbox.panelTitle`，繁体同步。 |
| `zh-CN.ts:5330` | 产物 | 生成的文件 | `chat.sandbox.tabArtifacts`，繁体同步。 |
| `zh-CN.ts:5334` | 产物范围 | 查看哪些文件 | `chat.sandbox.artifactScope`，繁体同步。 |
| `zh-CN.ts:5342` | 当前沙箱配置不支持桌面。需要在工作区沙箱设置的模板列表里选中带桌面的镜像，且后端为 Cube 或 E2B。 | 当前工具环境未开通桌面功能，请联系管理员。 | `chat.sandbox.desktopUnsupported`，繁体同步。 |
| `zh-CN.ts:5350` | 桌面空闲已断开，沙箱将按工作区 TTL 自行暂停。可重新连接。 | 桌面因空闲已断开，可以重新连接。 | `chat.sandbox.desktopIdleDisconnected`，繁体同步。 |
| `zh-CN.ts:5365` | 终端空闲已断开，其中的 shell 已结束（nohup 启动的后台任务不受影响）。重新连接会打开一个新的 shell。 | 终端因空闲断开，当前终端已结束。另行启动的后台任务会继续运行。重新连接会打开一个新终端。 | `chat.sandbox.idleDisconnectedNewShell`，繁体同步。 |
| `zh-CN.ts:5366` | 连接已断开，终端中的 shell 已结束（nohup 启动的后台任务不受影响）。重新连接会打开一个新的 shell。 | 连接已断开，当前终端已结束。另行启动的后台任务会继续运行。重新连接会打开一个新终端。 | `chat.sandbox.disconnectedNewShell`，繁体同步。 |
| `zh-CN.ts:5384` | 无法回滚：当前有沙箱，但没有可回退的检查点 | 当前对话没有保存可恢复的工作文件版本，无法撤回到这里。 | `chat.rewind.noCheckpoint`，繁体同步。 |
| `zh-CN.ts:7556` | 已挂载卷 | 已连接文件存储 | `settings.sandbox.cardVolumeMounted`，繁体同步。 |
| `zh-CN.ts:7574` | 构建已完成，但没有构建带 default 标签，创建沙箱时无法解析。请在 E2B 删除该模板，刷新后 工作台会重新构建。 | 缺少可启动的默认环境版本，请联系管理员检查运行环境模板。 | `settings.sandbox.templateUntaggedHint`，繁体同步。 |
| `zh-CN.ts:7590` | 该沙箱正在安装或移除 Skill，完成前不能更换或重建运行模板。 | 工具正在安装或移除，完成后才可以更换或重建运行环境。 | `settings.sandbox.templateLockedByInFlight`，繁体同步。 |
| `zh-CN.ts:9367` | 产物 | 生成的文件 | `artifactLibrary.title`，繁体同步。 |
| `zh-CN.ts:9401` | 产物加载失败 | 暂时无法读取文件列表，请重试。 | `artifactLibrary.loadFailed`，繁体同步。 |
| `zh-CN.ts:9405` | 还没有产物 | 这里还没有文件 | `artifactLibrary.empty.title`，繁体同步。 |
| `zh-CN.ts:9406` | 在对话中让智能体生成报告、表格或演示文稿，文件会出现在这里 | 在对话里生成的报告、表格或演示稿会出现在这里。 | `artifactLibrary.empty.description`，繁体同步。 |

### 空间与通用反馈

| 文件:行号 | 改前 | 改后 | 说明 |
|---|---|---|---|
| `zh-CN.ts:40` | 缺少对应翻译 | 成员姓名暂不可用 | `displayError.memberUnknown`，繁体同步。 |
| `zh-CN.ts:57` | 缺少对应翻译 | 查看诊断详情 | `displayError.diagnostics`，繁体同步。 |
| `zh-CN.ts:58` | 缺少对应翻译 | 暂时无法读取所需内容，请刷新后重试。 | `displayError.readFailed`，繁体同步。 |
| `zh-CN.ts:59` | 缺少对应翻译 | 暂时无法确认操作是否完成，请先刷新核对，再决定是否重试。 | `displayError.writeUnconfirmed`，繁体同步。 |
| `zh-CN.ts:65` | 缺少对应翻译 | 请重新选择要操作的标签。 | `displayError.selectTag`，繁体同步。 |
| `zh-CN.ts:66` | 缺少对应翻译 | 请重新打开要操作的常见问答。 | `displayError.selectFaq`，繁体同步。 |
| `zh-CN.ts:886` | 查看您的账户基础信息（用户 ID、用户名、邮箱、注册时间），并可修改登录密码 | 查看用户名、邮箱和注册时间，也可以修改密码。 | `userProfile.description`，繁体同步。 |
| `zh-CN.ts:902` | 您的账号通过 OIDC 登录开通，尚未设置本地登录密码。 | 你的账号使用单位统一登录，尚未设置工作台密码。 | `userProfile.changePassword.oidcOnlyDescription`，繁体同步。 |
| `zh-CN.ts:903` | 请联系系统管理员重置密码，或继续使用 OIDC 登录。自助修改密码需要先知道当前密码。 | 可以继续使用单位账号登录。如需设置工作台密码，请联系系统管理员。 | `userProfile.changePassword.oidcOnlyNotice`，繁体同步。 |
| `zh-CN.ts:4531` | 流式连接失败 | 接收回答时连接中断，请重新打开对话查看已有内容。 | `error.streamFailed`，繁体同步。 |
| `zh-CN.ts:4537` | 缺少对应翻译 | 空间未能创建。请核对空间名称或联系管理员获取邀请。 | `error.tenant.createFailed`，繁体同步。 |
| `zh-CN.ts:4538` | 缺少对应翻译 | 暂时无法读取空间密钥列表，请刷新。 | `error.tenant.listApiKeysFailed`，繁体同步。 |
| `zh-CN.ts:4539` | 缺少对应翻译 | 暂时无法确认密钥是否已创建，请先刷新列表。 | `error.tenant.createApiKeyFailed`，繁体同步。 |
| `zh-CN.ts:4540` | 缺少对应翻译 | 暂时无法确认密钥是否已删除，请刷新后核对。 | `error.tenant.deleteApiKeyFailed`，繁体同步。 |
| `zh-CN.ts:4561` | 刷新Token失败 | 暂时无法确认登录状态，请重试。 | `error.auth.refreshTokenFailed`，繁体同步。 |
| `zh-CN.ts:4563` | Token验证失败 | 暂时无法确认登录状态，请重试。 | `error.auth.validateTokenFailed`，繁体同步。 |

### 对话与引用

| 文件:行号 | 改前 | 改后 | 说明 |
|---|---|---|---|
| `zh-CN.ts:80` | 缺少对应翻译 | 暂未记录单位依据，请核对原表。 | `evidence.unitBasisUnknown`，繁体同步。 |
| `zh-CN.ts:81` | 缺少对应翻译 | 数值 | `evidence.cellValue`，繁体同步。 |
| `zh-CN.ts:82` | 缺少对应翻译 | 来源 | `evidence.cellSource`，繁体同步。 |
| `zh-CN.ts:83` | 缺少对应翻译 | 请查看原件 | `evidence.cellOpenOriginal`，繁体同步。 |
| `zh-CN.ts:84` | 缺少对应翻译 | 使用筛选和计算前读到的记录 | `evidence.loadedInputs`，繁体同步。 |
| `zh-CN.ts:85` | 缺少对应翻译 | 使用本次查询返回的记录 | `evidence.returnedSourceRows`，繁体同步。 |
| `zh-CN.ts:86` | 缺少对应翻译 | 横轴 | `evidence.axisX`，繁体同步。 |
| `zh-CN.ts:87` | 缺少对应翻译 | 纵轴 | `evidence.axisY`，繁体同步。 |
| `zh-CN.ts:88` | 缺少对应翻译 | 查看诊断详情 | `evidence.adminDetails`，繁体同步。 |
| `zh-CN.ts:89` | 缺少对应翻译 | 这条旧回答没有保存当时的文件版本，无法定位当时的原文。 | `evidence.historicalVersionMissing`，繁体同步。 |
| `zh-CN.ts:90` | 缺少对应翻译 | 本次预览无法核对完整文件，请从知识库打开原件核对。 | `evidence.previewLimit`，繁体同步。 |
| `zh-CN.ts:91` | 缺少对应翻译 | 无法打开回答时使用的版本，请重新选择来源并核对引用。 | `evidence.sourceVersionChanged`，繁体同步。 |
| `zh-CN.ts:94` | 查看来源，供核查 | 查看本次查到的数据 | `evidence.checkingButton`，繁体同步。 |
| `zh-CN.ts:95` | 回答尚未完成自动审核。以下是本轮实际返回的数据，仅供核查，不代表已验证相邻表述。 | 下方是这次查询取得的数据。带标记的说法仍待核对，请对照原表或原文确认。 | `evidence.unverifiedReview`，繁体同步。 |
| `zh-CN.ts:98` | 暂时无法读取这条依据。 | 暂时无法读取这条依据，请关闭后重新打开。 | `evidence.loadFailed`，繁体同步。 |
| `zh-CN.ts:99` | 来源不可用 | 暂时无法打开这份来源，请核对文件是否仍可访问。 | `evidence.unavailable`，繁体同步。 |
| `zh-CN.ts:100` | 已保存的执行结果与原件版本 | 回答时使用的数据和原件版本 | `evidence.snapshot`，繁体同步。 |
| `zh-CN.ts:105` | 来源声明 | 来源标注 | `evidence.declaredUnit`，繁体同步。 |
| `zh-CN.ts:107` | 列单位依据 | 查看各列的单位依据 | `evidence.columnUnits`，繁体同步。 |
| `zh-CN.ts:108` | 声明层级 | 单位从哪里得出 | `evidence.unitLevel`，繁体同步。 |
| `zh-CN.ts:109` | 单位表达式 | 单位计算方式 | `evidence.unitExpression`，繁体同步。 |
| `zh-CN.ts:110` | 已审核表格方案中的单位声明 | 来自已核对的表格设置 | `evidence.reviewedPlanUnit`，繁体同步。 |
| `zh-CN.ts:111` | 导入数据的系列元数据 | 来自导入时的指标说明 | `evidence.importedSeriesUnit`，繁体同步。 |
| `zh-CN.ts:112` | 确定性计算表达式 | 由计算公式得出 | `evidence.expressionUnit`，繁体同步。 |
| `zh-CN.ts:113` | 输入范围 | 读取范围 | `evidence.inputScopeLabel`，繁体同步。 |
| `zh-CN.ts:114` | 加载的输入数 | 本次读取的记录数 | `evidence.inputCountLabel`，繁体同步。 |
| `zh-CN.ts:116` | 实际绘图映射（原表坐标从0起算） | 绘图使用的数据 | `evidence.mappingLabel`，繁体同步。 |
| `zh-CN.ts:117` | 来源输入或返回结果不完整，此图不能代表完整范围。 | 本次只使用了部分数据，结果不能代表完整范围。 | `evidence.partialSource`，繁体同步。 |
| `zh-CN.ts:118` | 本次查询或处理返回的数据，具体口径见下方依据。 | 这是本次查到或计算出的数据。 | `evidence.calculated`，繁体同步。 |
| `zh-CN.ts:119` | 查询与处理依据 | 查看读取范围和计算依据 | `evidence.calculation`，繁体同步。 |
| `zh-CN.ts:120` | 本次处理加载的输入范围，不代表已识别每个贡献单元格。 | 这里列出本次读取的数据范围，部分结果无法逐一定位到原单元格。 | `evidence.inputScope`，繁体同步。 |
| `zh-CN.ts:122` | 原件版本无法核验或读取。 | 暂时无法打开回答时使用的原件版本，请重试或联系资料负责人。 | `evidence.previewFailed`，繁体同步。 |
| `zh-CN.ts:126` | 没有记录精确单元格坐标。 | 未记录确切的单元格位置，请打开原件核对。 | `evidence.noCoordinates`，繁体同步。 |
| `zh-CN.ts:2190` | 已加载 {fetched} / {total} 个分块 | 已读取 {fetched}/{total} 段正文 | `agentStream.knowledgeChunksList.chunkRange`，繁体同步。 |
| `zh-CN.ts:2192` | 第 {from}–{to} 块 | 当前显示第 {from} 至 {to} 段 | `agentStream.knowledgeChunksList.offsetRange`，繁体同步。 |
| `zh-CN.ts:2204` | 命中 {count} 条候选，相关性不足，未用于回答 | 找到 {count} 段可能相关的内容，但与问题联系不够明确，本次回答未引用。 | `agentStream.search.candidatesBelowThreshold`，繁体同步。 |
| `zh-CN.ts:2572` | OAuth 授权 · {service} | 登录并授权 {service} | `agentStream.mcpOAuth.titleWithService`，繁体同步。 |
| `zh-CN.ts:2573` | OAuth 授权 · {service} › {tool} | 登录并授权 {service} 使用 {tool} | `agentStream.mcpOAuth.titleWithTool`，繁体同步。 |
| `zh-CN.ts:2574` | 点击授权将打开新窗口完成登录，授权成功后会自动继续本次工具调用。 | 工作台会打开服务提供方的登录窗口，请核对授权用途。授权后会继续这次操作。 | `agentStream.mcpOAuth.desc`，繁体同步。 |
| `zh-CN.ts:2575` | 去授权 | 登录并授权 | `agentStream.mcpOAuth.authorize`，繁体同步。 |
| `zh-CN.ts:2627` | 调用参数 | 查看本次操作的详细内容 | `agentStream.toolApproval.argsLabel`，繁体同步。 |
| `zh-CN.ts:2644` | 参数不是合法 JSON | 填写的内容格式有误，请核对后再允许。 | `agentStream.toolApproval.invalidJson`，繁体同步。 |
| `zh-CN.ts:5273` | 缺少对应翻译 | 原网页 | `chat.webOriginalUrl`，繁体同步。 |
| `zh-CN.ts:5274` | 缺少对应翻译 | 暂时无法确认网页状态 | `chat.webUnknownState`，繁体同步。 |
| `zh-CN.ts:5275` | 缺少对应翻译 | 第 {index} 项输入 | `chat.approvalInput`，繁体同步。 |
| `zh-CN.ts:5276` | 缺少对应翻译 | 请展开查看完整内容 | `chat.approvalGroupedValue`，繁体同步。 |
| `zh-CN.ts:5277` | 缺少对应翻译 | 文字 | `chat.parameterText`，繁体同步。 |
| `zh-CN.ts:5278` | 缺少对应翻译 | 数字 | `chat.parameterNumber`，繁体同步。 |
| `zh-CN.ts:5279` | 缺少对应翻译 | 是或否 | `chat.parameterYesNo`，繁体同步。 |
| `zh-CN.ts:5280` | 缺少对应翻译 | 列表 | `chat.parameterList`，繁体同步。 |
| `zh-CN.ts:5281` | 缺少对应翻译 | 一组信息 | `chat.parameterGroup`，繁体同步。 |
| `zh-CN.ts:5282` | 缺少对应翻译 | 复制诊断信息 | `chat.requestInfoCopy`，繁体同步。 |
| `zh-CN.ts:5283` | 缺少对应翻译 | 可查看的来源 | `chat.requestInfoSources`，繁体同步。 |
| `zh-CN.ts:5284` | 缺少对应翻译 | 查看原文摘录 | `chat.relatedSourceExcerpt`，繁体同步。 |
| `zh-CN.ts:5285` | 缺少对应翻译 | 网页内容未能读取，请打开原网页核对。 | `chat.webReadFailed`，繁体同步。 |
| `zh-CN.ts:5286` | 缺少对应翻译 | 网页已读取，摘要未生成。请打开原网页查看。 | `chat.webSummaryFailed`，繁体同步。 |
| `zh-CN.ts:5287` | 缺少对应翻译 | 本次操作未能完成，请核对输入后重试。 | `chat.executionFailed`，繁体同步。 |
| `zh-CN.ts:5288` | 缺少对应翻译 | 当前回答还没有可复制的正文。 | `chat.copyEmptyAnswer`，繁体同步。 |
| `zh-CN.ts:5289` | 缺少对应翻译 | 当前回答还没有可保存的正文。 | `chat.saveEmptyAnswer`，繁体同步。 |
| `zh-CN.ts:5290` | 缺少对应翻译 | 已打开编辑器，请选择知识库后保存。 | `chat.editorOpened`，繁体同步。 |
| `zh-CN.ts:5392` | 请求信息 | 回答信息 | `chat.requestInfoTitle`，繁体同步。 |
| `zh-CN.ts:5393` | Request ID | 请求编号 | `chat.requestInfoRequestId`，繁体同步。 |
| `zh-CN.ts:5394` | 消息 ID | 消息编号 | `chat.requestInfoMessageId`，繁体同步。 |
| `zh-CN.ts:5395` | 会话 ID | 对话编号 | `chat.requestInfoSessionId`，繁体同步。 |
| `zh-CN.ts:5397` | 发起时间 | 提问时间 | `chat.requestInfoSentAt`，繁体同步。 |
| `zh-CN.ts:5398` | 暂无请求信息 | 本条回答暂无可显示的信息。 | `chat.requestInfoEmpty`，繁体同步。 |
| `zh-CN.ts:5409` | 已高亮核验通过的原文片段，部分引用内容尚未匹配 | 已标出能在原文找到的文字，其余引用仍待核对。 | `chat.referenceSourcePartial`，繁体同步。 |
| `zh-CN.ts:5410` | 已打开同源父段落所在第 {page} 页，引用文字尚未精确定位 | 已打开相关段落所在的第 {page} 页，尚未找到引用的确切句子。 | `chat.referenceSourceParentContext`，繁体同步。 |
| `zh-CN.ts:5419` | 片段ID: | 原文摘录编号 | `chat.chunkIdLabel`，繁体同步。 |
| `zh-CN.ts:5420` | 文档ID: | 文档编号 | `chat.documentIdLabel`，繁体同步。 |
| `zh-CN.ts:5421` | FAQ ID: | 问答记录编号 | `chat.faqIdLabel`，繁体同步。 |
| `zh-CN.ts:5422` | 所属文档ID: | 所属文档编号 | `chat.faqContainerIdLabel`，繁体同步。 |
| `zh-CN.ts:5427` | 片段 #{index} | 第 {index} 段摘录 | `chat.chunkIndexLabel`，繁体同步。 |
| `zh-CN.ts:5428` | (位置: {position}) | 摘录顺序 {position} | `chat.chunkPositionLabel`，繁体同步。 |
| `zh-CN.ts:5482` | 缺少对应翻译 | 后台任务 | `chat.backgroundWork.untitled`，繁体同步。 |
| `zh-CN.ts:5483` | 缺少对应翻译 | 后台运行的操作 | `chat.backgroundWork.operation`，繁体同步。 |

### 知识库、上传和处理进度

| 文件:行号 | 改前 | 改后 | 说明 |
|---|---|---|---|
| `zh-CN.ts:4177` | {ok} 个完成，{bad} 个未成功 | {ok} 份已完成，{bad} 份需要查看 | `uploadTasks.titleDoneWithIssues`，繁体同步。 |
| `zh-CN.ts:4203` | 可检索，后台优化中 | 正文已可搜索，其他内容仍在整理 | `uploadTasks.phaseFinalizing`，繁体同步。 |
| `zh-CN.ts:4205` | 可用内容已就绪，{count} 项需要处理 | 部分内容已可用，还有 {count} 项待核对 | `uploadTasks.attentionReady`，繁体同步。 |
| `zh-CN.ts:4206` | 上传失败 | 文件未上传成功，请重试上传 | `uploadTasks.phaseUploadFailed`，繁体同步。 |
| `zh-CN.ts:4207` | 解析失败 | 原件已保存，内容解析未完成，请重试解析 | `uploadTasks.phaseParseFailed`，繁体同步。 |
| `zh-CN.ts:6752` | 缺少对应翻译 | 未能下载原件，请从知识库重新打开这份文档后再试。 | `file.downloadFailed`，繁体同步。 |
| `zh-CN.ts:8486` | 处理流水线 | 文件处理进度 | `knowledgeStages.title`，繁体同步。 |
| `zh-CN.ts:8487` | 知识处理 | 文件处理 | `knowledgeStages.root`，繁体同步。 |
| `zh-CN.ts:8491` | {stage}阶段失败 | {stage}未能完成 | `knowledgeStages.stageFailed`，繁体同步。 |
| `zh-CN.ts:8492` | 复制错误信息 | 复制诊断信息 | `knowledgeStages.copyError`，繁体同步。 |
| `zh-CN.ts:8504` | LIVE | 正在更新 | `knowledgeStages.live`，繁体同步。 |
| `zh-CN.ts:8508` | 查看 Trace | 查看处理进度 | `knowledgeStages.viewTrace`，繁体同步。 |
| `zh-CN.ts:8519` | 文档解析服务不可用 | 文件解析服务暂不可用 | `knowledgeStages.errorCode.DOCREADER_UNAVAILABLE`，繁体同步。 |
| `zh-CN.ts:8520` | 无法连接 DocReader，或连接已中断。请检查解析服务是否启动、是否反复重启以及网络是否正常；服务恢复后再重试，无需重复上传文件。 | 请在服务恢复后点“重新解析”，无需再次上传。 | `knowledgeStages.errorCode.DOCREADER_UNAVAILABLE_SUGGESTION`，繁体同步。 |
| `zh-CN.ts:8521` | 文档解析超时 | 文件未能在限定时间内读完 | `knowledgeStages.errorCode.DOCREADER_TIMEOUT`，繁体同步。 |
| `zh-CN.ts:8522` | 请检查 DocReader 的健康状态和负载；服务正常后再重试，必要时拆分大文件。 | 请点“重新解析”，或把大文件拆小后再上传。 | `knowledgeStages.errorCode.DOCREADER_TIMEOUT_SUGGESTION`，繁体同步。 |
| `zh-CN.ts:8523` | 文档解析失败 | 未能读完文件内容 | `knowledgeStages.errorCode.DOCREADER_PARSE_FAILED`，繁体同步。 |
| `zh-CN.ts:8524` | 请检查文件格式，并让管理员查看本次解析对应的 DocReader 日志。 | 请确认原件可以打开，再点“重新解析”。如果仍失败，请联系管理员。 | `knowledgeStages.errorCode.DOCREADER_PARSE_FAILED_SUGGESTION`，繁体同步。 |
| `zh-CN.ts:8526` | 解析服务的磁盘、临时目录或共享内存空间不足。请联系管理员清理或扩容后重试。 | 文件解析空间不足，请联系管理员处理后再重试。 | `knowledgeStages.errorCode.DOCREADER_STORAGE_EXHAUSTED_SUGGESTION`，繁体同步。 |
| `zh-CN.ts:8527` | 长时间无进展，已自动终止 | 文件处理长时间没有进展，已停止 | `knowledgeStages.errorCode.TASK_STALLED`，繁体同步。 |
| `zh-CN.ts:8528` | 处理超过阈值仍没有任何进展，且队列中已无对应任务，已被系统标记为失败。请点击「重试」；如反复出现，请检查该阶段依赖的服务（文档解析、模型、向量库）是否正常。 | 请点“重新解析”。如果仍失败，请联系管理员。 | `knowledgeStages.errorCode.TASK_STALLED_SUGGESTION`，繁体同步。 |
| `zh-CN.ts:8529` | 请查看应用日志获取详细信息。 | 请点“重新解析”。如果仍失败，请联系管理员。 | `knowledgeStages.errorCode.UNKNOWN_SUGGESTION`，繁体同步。 |
| `zh-CN.ts:8532` | 缺少对应翻译 | 暂时无法确认进度，请刷新查看。 | `knowledgeStages.status.unknown`，繁体同步。 |
| `zh-CN.ts:8535` | 优化中 | 正文已可搜索，仍在整理 | `knowledgeStages.status.finalizing`，繁体同步。 |
| `zh-CN.ts:8542` | 缺少对应翻译 | 其他处理步骤 | `knowledgeStages.stage.other`，繁体同步。 |
| `zh-CN.ts:8544` | 分块 | 整理正文段落 | `knowledgeStages.stage.chunking`，繁体同步。 |
| `zh-CN.ts:8545` | 向量化 | 准备正文搜索 | `knowledgeStages.stage.embedding`，繁体同步。 |
| `zh-CN.ts:8546` | 多模态识别 | 识别图片等内容 | `knowledgeStages.stage.multimodal`，繁体同步。 |
| `zh-CN.ts:8547` | 后处理 | 完成其余整理 | `knowledgeStages.stage.postprocess`，繁体同步。 |
| `zh-CN.ts:8550` | 缺少对应翻译 | 模型处理 | `knowledgeStages.detail.generation`，繁体同步。 |
| `zh-CN.ts:8551` | 缺少对应翻译 | 共 {n} 项 | `knowledgeStages.detail.itemCount`，繁体同步。 |
| `zh-CN.ts:8555` | 起点偏移 | 从开始处理算起 | `knowledgeStages.detail.offset`，繁体同步。 |
| `zh-CN.ts:8556` | 时序 | 处理时间 | `knowledgeStages.detail.timing`，繁体同步。 |
| `zh-CN.ts:8557` | 身份信息 | 内部任务记录 | `knowledgeStages.detail.identity`，繁体同步。 |
| `zh-CN.ts:8558` | 阶段分解 | 各步骤进度 | `knowledgeStages.detail.stageBreakdown`，繁体同步。 |
| `zh-CN.ts:8560` | 子 span 数 | 子任务数 | `knowledgeStages.detail.childCount`，繁体同步。 |
| `zh-CN.ts:8567` | Trace 级元数据 | 诊断关联信息 | `knowledgeStages.detail.traceMetadata`，繁体同步。 |
| `zh-CN.ts:8568` | 记录与 Langfuse 等可观测性系统关联的辅助字段（如 trace ID）。各阶段的业务入参/出参在「输入」「输出」标签中查看。 | 用于将本次处理记录与诊断系统中的记录对应起来。 | `knowledgeStages.detail.metadataHint`，繁体同步。 |
| `zh-CN.ts:8569` | 当前 span 没有元数据。阶段与子任务的入参/出参请查看「输入」「输出」；若已接入 Langfuse，trace 级字段会显示在「概览」。 | 这一步没有额外的诊断记录。 | `knowledgeStages.detail.metadataEmpty`，繁体同步。 |
| `zh-CN.ts:8574` | 此阶段未记录详细 span，仅展示推断状态。 | 这一步没有单独的进度记录，页面根据整体进度估计。 | `knowledgeStages.detail.placeholderHint`，繁体同步。 |
| `zh-CN.ts:8581` | 原始 JSON | 诊断原始记录 | `knowledgeStages.tab.raw`，繁体同步。 |
| `zh-CN.ts:8584` | 已 {minutes} 分钟没有进展，可能已卡住 | 进度已有 {minutes} 分钟未更新 | `knowledgeStages.stall.title`，繁体同步。 |
| `zh-CN.ts:8585` | 可以继续等待，或停止解析后重建知识。长时间仍无进展时，系统会自动将其标记为失败。 | 可以继续等待，也可以停止后重新解析。 | `knowledgeStages.stall.hint`，繁体同步。 |
| `zh-CN.ts:8586` | 当前停在「{stage}」阶段。可以继续等待，或停止解析后重建知识。长时间仍无进展时，系统会自动将其标记为失败。 | 当前步骤为“{stage}”。可以继续等待，也可以停止后重新解析。 | `knowledgeStages.stall.hintAtStage`，繁体同步。 |
| `zh-CN.ts:8587` | 已 {minutes} 分钟没有进展，任务仍在排队 | 已经等待 {minutes} 分钟，文件仍在排队 | `knowledgeStages.stall.queuedTitle`，繁体同步。 |
| `zh-CN.ts:8588` | 这篇文档还有任务在队列中等待处理，通常是任务积压导致，稍后会自动继续，一般无需操作。 | 轮到这份文件时会自动处理，可以稍后回来查看。 | `knowledgeStages.stall.queuedHint`，繁体同步。 |
| `zh-CN.ts:8604` | 图像索引 Embedding 模型 | 准备图片搜索所用的模型 | `uploadConfirm.imageEmbeddingModel`，繁体同步。 |
| `zh-CN.ts:8605` | 图像索引使用知识库绑定的 Embedding 模型；如需更换，请前往知识库设置。 | 准备图片搜索时沿用知识库所选的搜索模型。可在知识库设置中更换。 | `uploadConfirm.imageIndexInherited`，繁体同步。 |
| `zh-CN.ts:8606` | 本次图片向量索引 | 为本次上传的图片准备搜索数据 | `uploadConfirm.imageVectorIndex`，繁体同步。 |
| `zh-CN.ts:8607` | 为本次上传或重新解析的图片生成向量，不改变知识库默认配置。是否参与检索仍由知识库的“图片向量检索”开关决定。 | 为本次上传或重新解析的图片准备搜索数据。图片能否被搜到，还取决于知识库是否已开启图片搜索。 | `uploadConfirm.imageVectorIndexHint`，繁体同步。 |
| `zh-CN.ts:8608` | 请先在知识库设置中开启向量索引。 | 请先在知识库设置中开启按意思搜索。 | `uploadConfirm.imageVectorRequiresIndex`，繁体同步。 |
| `zh-CN.ts:8620` | 父子分块 | 按短段查找，回答时带上前后文 | `uploadConfirm.summaryParentChildShort`，繁体同步。 |
| `zh-CN.ts:8624` | 分块 {size} | 每段最多 {size} 个字符 | `uploadConfirm.navChunkingSummary`，繁体同步。 |
| `zh-CN.ts:8642` | 含图片内容，请启用多模态并选择模型 | 要识别图片里的内容，请开启图片识别并选择模型。 | `uploadConfirm.multimodalSetupHint`，繁体同步。 |
| `zh-CN.ts:8643` | 含音频内容，请启用语音识别并选择模型 | 要把录音转成文字，请开启语音识别并选择模型。 | `uploadConfirm.asrSetupHint`，繁体同步。 |
| `zh-CN.ts:8644` | 请配置多模态模型 | 请在知识库设置中配置图片识别模型。 | `uploadConfirm.vlmModelRequired`，繁体同步。 |
| `zh-CN.ts:8645` | 请配置语音识别模型 | 请在知识库设置中配置语音识别模型。 | `uploadConfirm.asrModelRequired`，繁体同步。 |
| `zh-CN.ts:8646` | 已启用多模态，请选择 VLM 模型 | 要识别图片里的内容，请选择图片识别模型。 | `uploadConfirm.vlmModelSelectRequired`，繁体同步。 |
| `zh-CN.ts:8647` | 已启用语音识别，请选择 ASR 模型 | 要把录音转成文字，请选择语音识别模型。 | `uploadConfirm.asrModelSelectRequired`，繁体同步。 |
| `zh-CN.ts:8669` | 缺少对应翻译 | 请先选择要导出的知识库。 | `knowledgeBase.selectKnowledgeBase`，繁体同步。 |
| `zh-CN.ts:8825` | 确定要删除这个问题吗？删除后将同时移除对应的向量索引。 | 删除后，这条问答将无法再被搜到。 | `knowledgeBase.confirmDeleteQuestion`，繁体同步。 |
| `zh-CN.ts:8827` | 自定义元数据 | 文档补充信息 | `knowledgeBase.customMetadata`，繁体同步。 |
| `zh-CN.ts:8828` | 用于文档总结，并在文档被召回后作为文档级上下文辅助回答；不会参与索引或影响召回排序 | 补充信息 | `knowledgeBase.metadataCapabilityHint`，繁体同步。 |
| `zh-CN.ts:8835` | 布尔值 | 是／否 | `knowledgeBase.metadataTypeBoolean`，繁体同步。 |
| `zh-CN.ts:8845` | 索引同步失败 | 搜索结果暂未更新 | `knowledgeBase.indexFailed`，繁体同步。 |
| `zh-CN.ts:8846` | 内容已保存，但索引同步失败，请点击重试索引 | 内容已保存，搜索结果暂未更新。点“更新搜索结果”重试。 | `knowledgeBase.chunkSavedIndexFailed`，繁体同步。 |
| `zh-CN.ts:8847` | 分块已被他人修改，列表已刷新，请重试 | 这段内容已被其他人修改，请查看最新内容后再编辑。 | `knowledgeBase.chunkEditConflict`，繁体同步。 |
| `zh-CN.ts:8848` | 重试索引 | 更新搜索结果 | `knowledgeBase.retryIndex`，繁体同步。 |
| `zh-CN.ts:8896` | 没有兼容的目标知识库（需要相同类型和 Embedding 模型） | 暂时没有可接收这份文档的知识库，请联系管理员核对目标库设置。 | `knowledgeBase.moveNoTargets`，繁体同步。 |
| `zh-CN.ts:8897` | 复用向量（快速） | 沿用原有搜索数据 | `knowledgeBase.moveModeReuseVectors`，繁体同步。 |
| `zh-CN.ts:8898` | 直接移动分块和向量索引，适用于分片配置相同的情况 | 移动较快，适合正文整理方式相同的知识库。 | `knowledgeBase.moveModeReuseVectorsDesc`，繁体同步。 |
| `zh-CN.ts:8899` | 重新解析 | 按目标设置重新整理 | `knowledgeBase.moveModeReparse`，繁体同步。 |
| `zh-CN.ts:8900` | 使用目标知识库的分片配置重新解析文档 | 按目标知识库的设置重新整理文档并准备搜索数据。 | `knowledgeBase.moveModeReparseDesc`，繁体同步。 |

## 有意保留的其他盘点项

| 盘点条目 | 原因 |
|---|---|
| Go 登录、邀请、下载、FAQ、PDF 设置、存储检查和异常恢复的协议文字 | 已知失败原因在使用它的页面改写。接口原始值供既有调用方使用。旧系统存储检查在当前前端无直接使用入口，现用文件存储设置已映射。 |
| 表格和报告的公式、业务编码、文件名、原件版本和实际单元格 | 这些信息用于复核与重现，已有来源按原意保留。 |
| 研究指标冲突的批量确认 | 现有桥接协议按批次确认，页面呈现真实的旧值、新值和单位，已有逐行保存路径继续逐行选择。 |
| 自定义定时规则、模型和网络设置中的供应商正式用语 | 高级编辑与外部配置需要对应正式名称，旁边给出用途说明。任意自定义定时规则的列表使用简明名称，保留高级编辑与运行时间预览。 |
| 未识别的既有业务响应及保存的历史正文 | 各展示层只对掌握其含义的业务状态作区分。历史答案不重写，现有结构化值不从文字猜测。 |
| 商品研究与表格分析独立页面 | 两组独立页面沿用其现有中文页面结构。主程序和独立嵌入聊天的简繁语言包均同步。 |

## 验证与交付

盘点基线为 `4e2cd87c`。工作期间分支出现外部检查点 `48d5d98c` 和 `3430a361`。当前源码位于 `3430a361`，本审计文档包含该检查点之后的补充。本次任务未执行 Git 提交或推送，也未操作开发或生产服务。相对盘点基线共记录 121 组展示修改和 414 个简繁同步的翻译键，部分记录指向同一处文案。

| 检查 | 结果 |
|---|---|
| 前端 `npm run type-check` | 通过。构建缓存和依赖映射临时放在本工作树，结束后恢复原依赖链接。 |
| 前端 `src/**/*.test.ts`、`build/**/*.test.ts` | 346 个测试文件通过。沙箱限制 `tsx` 命令创建本地套接字，改用 `node --import tsx --test` 运行同一批文件。 |
| 前端主题样式检查和 `npm run build` | 通过。构建后已删除 `dist`。 |
| 商品研究 pytest | 5,356 项。全量运行 5,350 项通过，2 项跳过。全量运行启动后修正的 3 条文案断言在 41 项定向回归中通过。剩余 1 项为管理客户端重定向测试，沙箱禁止创建本地 TCP 套接字。用同一客户端和进程内模拟响应验证了拒绝重定向行为。测试使用指定 Python 和本工作树源码，事件循环唤醒改用只在测试进程生效的管道。 |
| 表格分析相关 pytest | 220 项通过。使用只影响测试进程的管道代替事件循环唤醒套接字，本工作树的 `structured-data/src` 用作 `PYTHONPATH`。 |
| Go `./internal/agent` | 改动相关测试通过。全包命令遇到沙箱禁止的临时监听，排除 49 项依赖监听的测试后通过。 |

项目私有交接目录在当前环境不存在，未能写入 `PROGRESS.md`。
