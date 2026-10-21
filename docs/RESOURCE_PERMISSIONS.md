# 知识库与研究工作台成员权限

空间 RBAC 决定成员的能力上限，资源 ACL 决定其能否访问具体知识库或研究台。两类资源分别保存授权，修改其中一方不会自动修改另一方。

## 默认与权限合并

新建的人类用户资源保存显式私有策略：

```json
{"space_permission":"none","members":[]}
```

`space_permission` 支持 `none`、`viewer`、`editor`。指定成员记录使用真实 `user_id`，每人选择 `viewer` 或 `editor`。授权取并集，编辑包含查看；空间全员可编辑时，给某人单独设查看不会降低其已有编辑权限。

| 身份/授权 | 查看资源 | 编辑内容 | 改成员权限、重命名、删除整个资源 |
| --- | --- | --- | --- |
| 主人，空间 Contributor 及以上 | 是 | 是 | 是 |
| 主人，空间 Viewer | 是 | 否 | 否 |
| 指定/全空间查看成员 | 是 | 否 | 否 |
| 指定/全空间编辑成员，Contributor 及以上 | 是 | 是 | 否 |
| 已授权编辑但降为空间 Viewer | 是 | 否 | 否 |
| 未授权的空间 Admin/Owner | 否 | 否 | 否 |

文件和文件夹继承知识库权限，本轮没有新增文件级 ACL。个人对话、研究逻辑、判断、策略和日报草稿仍按当前账号隔离；共享研究台不会自动公开这些内容。产业图和共享结构编辑使用研究台编辑权限。日报导出到知识库，另外校验目标知识库写入权限。

成员被暂停、退出空间或账号停用后，原有资源授权不能取代有效空间身份。保存权限时拒绝非本空间有效成员，以及给 Viewer 新授予编辑；已有条目若失效，主人需先调整或移除再保存。

## 研究台与来源权限分离

研究台授权成员有来源知识库直接查看权限时，沿用直接读取。没有知识库直接查看权限时，需要来源库主人明确批准该研究台分享指标投影：

1. 台主人在“成员与权限”发起来源共享请求。请求明确向库主人展示审批所需的台名称和主人标识，不授予库主人查看该台内容的权限。
2. 库主人在知识库“成员与权限”的请求列表批准或撤销。没有发起请求的其他私有研究台不会出现在该列表。
3. 获批研究台的授权成员可使用来源库已发布的结构化指标、观测及以此构成的产业图。不会取得来源库目录、原文件下载、全文检索或通用 SQL/工具代理权限。
4. 研究和结构化服务每次读取重新核验工作台 ACL、当前库主人审批和真实源文件/版本。审批撤销或库主人变更后，缓存不能继续放行。

批准作用于该台的授权成员范围，台主人随后调整台 ACL 不等于修改来源库 ACL。共享研究台的编辑者不能因此修改知识库、确认新的 Excel 区块或向库中写入文档。全文问答需独立具备来源库查看权。

## API 与服务边界

以下 API 需正常用户登录；资源权限管理不作为空间级 API Key 管理能力开放。

| API | 作用 |
| --- | --- |
| `GET/PUT /api/v1/knowledge-bases/:id/permissions` | 知识库主人读取/保存 ACL；GET 含明确请求的台审批列表 |
| `GET/PUT /api/v1/workspaces/research/:id/permissions` | 研究台主人读取/保存独立 ACL |
| `POST /api/v1/workspaces/research/:id/source-sharing/request` | 台主人申请来源分享 |
| `GET/PUT /api/v1/workspaces/research/:id/source-sharing` | 来源库主人查询、批准或撤销，PUT 为 `{"approved":true/false}` |
| `GET /api/v1/workspaces/research/:id/source-scope` | 为当前台成员生成最小研究数据范围，不返回原文件路径、标题或正文 |

ACL PUT 请求体为上述策略对象，接口沿用 `success/data` 包装。权限列表不随普通知识库/研究台对象对读者公开。

知识库响应给出有效 `my_permission`、`can_edit`、`can_manage`、`can_manage_permissions`；工作台给出 `can_edit`、`can_manage`、`can_manage_permissions`、`can_read_source`、`can_edit_source`、`can_manage_source` 和 `source_data_available`。前端以服务端明确结果为准，不能靠空间 Admin 或缓存创建者推导新权限。旧库的 `can_manage` 保留原管理规则，独立的 `can_manage_permissions` 仍只授予实际主人。

列表、详情、文件/目录、批量获取、检索、结构化查询和问答来源均需校验。原 `/files` 代理按真实文件绑定重新核验，ACL 保存撤销相关短时资源能力链接，重新签发也受当前权限约束。已经下载的副本和存储商已签发的预签名 URL 不属于可追回内容。

私有库不会因管理员配置 API Key 白名单或其他人的共享智能体而自动公开。现有组织级知识库共享仍按独立的显式共享校验；为新私有库建立组织共享需库主人权限。签名机器调用只可使用 Go 已核验的明确范围，不能伪装成研究台委托；新资源的机器访问还要求数据已对空间开放。

## 升级和兼容

- PostgreSQL 迁移：`000124_resource_permissions`；SQLite 迁移：`000042_resource_permissions`。
- 旧行的 `access_policy` 保持 SQL NULL，沿用旧的可见范围及既有管理规则；不会把历史回填的 `creator_id` 当成重新分配私有资源的依据。主人主动保存 ACL 后，转入新规则。
- 新资源与复制出的个人资源保存显式私有策略。既有显式 Default 系统绑定初始化的宏观库保持空间可读，不通过库名称放行。
- 旧台管理接口保留兼容规则，但 `can_manage_permissions` 仅由台主人获得，不能让旧台管理员打开可保存的 ACL 设置。
- Go、研究 Python、结构化 Python 和前端需作为同一版本部署。新 Python 遇到缺失的范围接口/签名绑定会拒绝访问，不回退旧数据。
- 回退迁移会删除新增 ACL 和来源授权字段。旧程序也不认识新 ACL，因此不能把直接回退旧版程序当成保留私有边界的方案；回退前需备份并限制访问，另行制定恢复步骤。

本轮没有新增回收站、归档、文件级授权或资源主人转让界面。知识库删除继续沿用现有绑定台级联删除规则。

## 验证

测试使用隔离临时数据库、本地 HTTP 测试服务和虚构账号，覆盖主人/成员/管理员、查看/编辑/管理、空间角色降级、旧资源保留、审批请求与撤销、来源变更、原链接访问、签名绑定、仅投影读取和迁移升级/回退。相关测试分布于 Go `internal/{types,application/access,application/service,handler,router,database}`，Python 两服务的 `tests/test_resource_permissions.py`，前端 `ResourcePermissionDrawer.test.ts` 与管理页测试。

前端通过真实 Vue/TDesign 组件离线浏览器验证；它不能代替后端鉴权测试。生产环境的跨账号验收及 PostgreSQL 实际迁移结果应与本地 SQLite 验证分开记录。
