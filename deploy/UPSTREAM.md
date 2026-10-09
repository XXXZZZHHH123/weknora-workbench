# WeKnora 上游更新

本项目使用源码快照维护 `WeKnora/`，不是普通 fork 或 submodule。更新器以 `.upstream/weknora.json` 中的提交为原始上游，以本项目 `WeKnora/` 为定制版本，以最新正式版为新版上游，调用 Git 三方合并。独立的 `structured-data/`、根部署配置、模型密钥和数据不会被上游目录覆盖。

当前初始上游是正式版 v0.8.2。GitHub 工作流 `WeKnora upstream update PR` 每天北京时间 10:20 检查，也支持手动运行；GitHub 调度可能延迟。只处理非草稿、非预发布版本。旧版本不会倒灌；不同分支的版本会标记 diverged，留待人工迁移。

有更新时创建 `codex/upstream-weknora-版本-提交` 分支和 PR，然后主动触发该分支的 CI（workflow_dispatch），确保默认 GITHUB_TOKEN 创建 PR 时也能运行检查。分支 CI 不推送生产镜像、不发布生产版本；确认 PR 并合并 main 后，才执行现有生产 CI/CD。更新器不调用审批或合并 API。

同一版本不重复创建 PR，有未完成的上游 PR 时不创建第二个，不覆盖手工修改，不强制推送。已关闭的同版本 PR 不自动重开，可在 GitHub 手动重新打开继续处理。部分运行已经推送分支但创建 PR 失败时，下一次运行会核对目标提交并恢复创建流程。

## 查看和预演

默认命令仅检查发布信息，不改工作树：

```sh
python3 scripts/sync_weknora.py
```

在工作树干净时准备本地更新分支（不切换当前分支、不推送、不影响运行容器）：

```sh
python3 scripts/sync_weknora.py --prepare
```

上游历史仅下载到临时 bare 仓库；本仓库只导入需要的源文件对象，不导入全部上游提交历史。保留被精简的官网目录策略：不重新引入 `website/` 与 `website-docs/`，本项目 `WeKnora/website-docs/README.md` 继续保留。

## 有冲突时

PR 会是草稿，包含冲突文件列表和 Git 生成的冲突标记。基线保持原提交，目标版本记入 `pending_update`。CI 会主动拒绝这种未完成更新，即使冲突只在不会编译的文档里，也不会把它当成可发布版本。

在更新分支逐项解决冲突后执行：

```sh
python3 scripts/sync_weknora.py --finalize
git add WeKnora .upstream/weknora.json
git commit -m 'Resolve WeKnora upstream update conflicts'
git push
```

finalize 只更新上游元数据，不自动提交；仍有冲突标记时会拒绝。检查通过并确认业务兼容性后，把草稿改为可审查 PR，再手动合并。

## 审查范围

无文本冲突不等于无业务冲突。重点检查定制 MCP、结构化工作台入口/登录、前端代理、数据库迁移、应用 Dockerfile 对 CentOS 7 的兼容性，以及 `deploy/image-builds.json` 中 docreader/PostgreSQL 等依赖版本；这些根部署配置不会被上游源码替换。需要调整时在同一 PR 中完成。现有 CI 不能替代配置真实模型后的业务验收。

工作流只使用内置的临时 GITHUB_TOKEN，不存放开发者或服务器 PAT。它需要 contents:write 创建分支、pull-requests:write 创建 PR、actions:write 触发分支 CI。仓库需允许 Actions 创建 PR；GitHub 将“创建和批准 PR”放在一个设置中，更新器只创建 PR，最终合并由用户决定。
