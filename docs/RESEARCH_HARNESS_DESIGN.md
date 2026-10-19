# Research question answering harness

This design uses the model to interpret the complete request and choose sources
and analysis. The runtime validates authorization, real identifiers, coverage,
units and calculations. It does not route business questions using keyword
lists or embed expected answers in production code.

## Why another iteration

A generator and a reviewer can see different projections of the same tool
history. Asking the generator to fetch the same evidence again does not repair
an omission in the reviewer's view. Synthetic review requests can also obscure
the real human conversation. Separately, removing a source reference from a
shared Wiki page does not remove that source's unsupported claims from its body.

The implementation separates these problems:

- Keep a turn-local human conversation for semantic review. Add a steer only
  after its real user message has been persisted. Keep the complete execution
  transcript separately for evidence checks. Do not identify internal messages
  by parsing the shape of user JSON.
- Expose omissions in bounded evidence views. When that view is incomplete,
  let the reviewer reopen immutable observations by a content-derived handle
  and an RFC 6901 JSON pointer. The reviewer chooses the needed evidence. This
  capability cannot run the generator's tools, read files or fetch other turns.
  It has a four-call limit and a 16,000-character source-content allowance;
  protocol envelopes are additional metadata. Failures and original source
  truncation remain visible and are never upgraded to complete evidence.
- Project exact successfully fetched chunks referenced by the candidate into a
  separate citation-first view. Chunk identity and original coverage survive
  body omissions; unknown or conflicting identities remain unverified. The
  view has a UTF-8 byte budget, including its identity lists. A successful source
  catalog also requires factual review of identity, counts and coverage; it
  cannot support claims about an unread document body. A separate compact
  directory companion preserves actual document identities and original
  pagination/filter scope without letting long descriptions hide later rows.
  Only an unfiltered first page that covers the actual total is marked as the
  complete directory; omissions keep immutable-reader handles when budget permits.
  This does not classify titles or choose business sources.
- Start a fresh bounded answer-repair allowance after a model-requested
  evidence-collection phase. Keep the total recovery and loop limits. Spending
  an allowance before the necessary evidence exists must not make a later
  correctable candidate impossible to repair.
- When a structured query's catalog unit is unknown, attach bounded original
  cells and chart series that actually intersect the returned value cells.
  These are evidence for model interpretation; they do not assign units,
  change values or relax comparison/conversion checks. Missing originals and
  incomplete chart views remain explicit.
  Native chart relationship extraction uses a bounded LRU keyed by canonical
  source path, verified SHA-256 and selected package parts, with same-key
  singleflight. A persisted chart index only recalls candidate parts; the runtime
  verifies their fresh worksheet/drawing/chart relationships and relationship
  types, then parses the selected original XML under explicit read budgets.
  Missing, legacy or unresolved index coverage remains incomplete and cannot
  establish chart absence. Unrelated worksheet payloads are not scanned for
  selected chart requests. Cached values
  are source metadata, never answers or question-based routing decisions.
  Value-series axis IDs and original axis labels remain separate evidence;
  formulas can support explicitly labeled dimensional inference by the model.
- A Wiki retraction must verify the surviving source text and independently
  review the revised body before persistence. Model failures, empty drafts
  and rejected reviews must retain retry evidence rather than consume the
  operation as successful. Reference cleanup alone is not semantic cleanup.

## Public designs used as references

Checked on 2026-10-17. These are mechanisms to evaluate, not a claim that one
framework is universally best or that Claude's private runtime was ported.

- [Anthropic context engineering](https://www.anthropic.com/engineering/effective-context-engineering-for-ai-agents):
  keep useful context compact and retrieve details when needed. Here, omitted
  review evidence remains addressable instead of disappearing after clipping.
- [Anthropic long-running harnesses](https://www.anthropic.com/engineering/effective-harnesses-for-long-running-agents):
  progress artifacts and actual completion checks improve continuity beyond
  model context alone. A task plan or a promise is not a completed action.
- [LangChain Deep Agents profiles](https://docs.langchain.com/oss/python/deepagents/profiles):
  provider/model adaptations should be explicit and evaluated. Changing a model
  requires comparable tests of protocol, source fidelity and final claims.
- [OpenHands conversation state](https://github.com/OpenHands/software-agent-sdk/blob/main/openhands-sdk/openhands/sdk/conversation/state.py):
  retain events and distinguish state from projections sent to the model.
- [SWE-agent trajectories](https://swe-agent.com/latest/usage/trajectories/):
  keep inspectable action/observation records. The project's maintenance status
  is a reason to borrow a mechanism rather than adopt the entire framework.
- [DeepSeek Harness](https://www.deepseek.com/harness/en/) and its
  [core interfaces](https://github.com/deepseek-ai/deepseek-harness/blob/master/packages/core/README.md):
  make the session, loop, prompts and tools separable. Its minimal coding
  scaffold is a useful ablation baseline, rather than a replacement for research
  tools. The [V4.1 Flash model card](https://huggingface.co/deepseek-ai/DeepSeek-V4.1-Flash)
  reports agent post-training and coding evaluations with Minimal; it does not
  establish that Minimal was the specific training runtime.
- [Anthropic's 2026 long-running design](https://www.anthropic.com/engineering/harness-design-long-running-apps)
  and [Managed Agents architecture](https://www.anthropic.com/engineering/managed-agents):
  separate execution, evaluation and durable session state. Extra boundaries
  and independent evaluations also add latency and coordination costs.
- [Deep Agents v0.7](https://www.langchain.com/blog/deep-agents-v0-7):
  evaluate leaner tool/prompt descriptions with task-level outcomes. Its reported
  token reduction does not establish an improvement for every model or task.

- [LongHarness preprint (October 9, 2026)](https://arxiv.org/abs/2610.13075):
  distinguish acquired evidence, evolving conclusions and remaining information
  gaps. Its evidence lifecycle is relevant to repeated searches and corrections
  lost in transcripts. This implementation keeps immutable observations and
  bounded recovery advice; it does not implement the paper's full persistent
  conclusion/evidence graph. The paper's corpus benchmarks are not validation
  of this application's tasks.

There is no controlled Claude comparison in this iteration. Model/tool
co-adaptation, evidence access and context quality are plausible contributors
to performance, not established causes of a measured gap. More instructions,
larger models or more tool calls are not acceptance criteria.

## Verification contract

Use synthetic fixtures from different subjects for protocol and authorization
boundaries. Run real source-based questions separately, preserving initial
failures and timeouts. Check claims and citations against originals, rather
than grading whether the answer resembles a template. Report incomplete
checks and skipped environment-dependent tests explicitly.

Business source files, account data, credentials and raw private conversations
belong outside the repository. This document records design; current test and
deployment status must be supplied by the actual execution report.

For large Wiki sources, the runtime processes real chunk windows in bounded
batches. The model selects quotations relevant to the previous page; the runtime
locates each quote exactly, records source/chunk hashes and Unicode ranges, and
checks the complete batch manifest. Overlap covers the maximum allowed quote
length. The final editor/reviewer receives exact excerpts and explicit selection
scope, rather than model summaries treated as source facts. Reading every listed
window proves mechanical coverage of that selection; it does not prove that a
model identified every semantically relevant passage. A missing quote alone
cannot establish source absence.

Pending or failed Wiki retractions have durable page markers. Question-answering
retrieval, direct chunk reads and linked summaries withhold the affected body
until verified; surviving original documents remain available. Shared page locks,
snapshot checks and source revalidation prevent an older rewrite from restoring
deleted provenance. Budget, protocol and source-change failures retain the marker.

## Original presentation preview

Physical slide location and visible original chart pixels are separate checks.
The browser resolves the presentation's actual slide order and picture
relationships, hashes the unchanged source blob, and lazily requests PNG
conversions for vector media on the selected citation page. It shows these as
individual original figures rather than claiming that a complete slide was
rasterized. Source/page changes cancel work and invalidate late callbacks.

The read-only endpoint uses the original file preview's tenant and KB read
authorization. The server checks the current source snapshot, uploaded file
fingerprint, requested source SHA and actual page-to-media relationship. The
document reader limits source/member size, conversion concurrency and time;
the caller verifies PNG binding, digest and dimensions. Output is private,
uncached HTTP media and does not alter stored documents, chunks or embeddings.
This does not resolve the separate cost of rasterizing all media during ingest.

An exact partial slide locator still identifies a verified physical page.
It may drive page scrolling, region highlighting and lazy original-media
selection. Precise text highlighting additionally requires a unique quote match
on that page. Approximate, missing, out-of-range and unavailable source mappings
remain unusable. Bad page/member selectors return a typed client error; a stale
source digest returns a conflict. Renderer/internal failures remain server errors.

Literal document search reports its actual expression, scan and continuation
scope. A zero-match text search is not semantic absence. The source critic uses
shared principles for task completion, evidence coverage, metric meaning and
source fidelity; review-only tool access is permitted before its final verdict.
No expression or source filename decides a research answer.

### Preserving source coverage through model projections

Document-tool Data and model-visible content are separate representations. An exact physical-page read now exposes its explicit `page_complete`, chunk-window offset, page-chunk count and continuation in the generator's context. A document pagination number alone cannot create a physical-page scope. Source-locator summaries preserve actual positions, mapping status, partial flags and revision hashes under a shared byte/item budget; they omit repeated quote text and disclose omissions. These are reported metadata, not an independent assertion that a whole passage or page was verified.

Citation review retains the actual query/page scope and source-plan metadata. When the same chunk body and offset are observed again, it stores bounded additional observations with their own immutable evidence handles instead of discarding a later scoped read. Different source identities, bodies or offsets retain the existing ambiguity protection. An internal chunk index never establishes a physical page number.

Original workbook evidence failures distinguish bounded-read exhaustion, source verification and other read stages. Public tool results expose a safe phase/type and requested scope; detailed exceptions remain in server logs. They retain successful stored query values without inventing source cells, units or absence claims. Read deadlines and integrity checks remain in force.

For original-file preview, a verified direct-parent relationship with one unambiguous current-source slide may supply a partial page-context location when the child has no locator. The UI explicitly distinguishes this from precise citation-text alignment; it retains the child's evidence and does not borrow the parent's quote as an exact match.

### Lossless structured row projection

The independent answer rewrite and the source reviewer receive an independently bounded companion for successful structured query executions. It uses the immutable result message and verifies that its data matches the executed tool result. Homogeneous row objects become an explicit column list and ordered value arrays; fields identical across every row are stored once as `constant_fields`, including explicit nulls. Heterogeneous rows remain objects so absent fields and nulls remain distinct. No question, candidate wording, metric value or expected answer selects rows.

The companion preserves source metadata, original result truncation and actual row counts. Its own byte budget and omitted row ranges describe projection completeness only. Each entry retains the exact evidence handle and original JSON Pointer; omitted row indexes are not character offsets for the existing reader. Separate source queries are never merged into an apparent continuous series. Deterministic calculation, source identity and numeric checks continue to consume the original execution data.

This addresses a failure where already fetched rows were repeatedly questioned after a bounded review view retained only an early prefix. Existing JIT reads had occurred, so the observation does not establish that JSON response mode disabled tool use. Runtime verification of the added companion remains a separate acceptance step.

### Repairing deterministic validation failures

For candidates subject to numeric provenance validation, that check runs after
the independent rewrite and before the semantic critic. A model's acceptance cannot override unmatched
numbers. A bounded diagnostic identifies exact lexical token candidates,
original byte/Unicode offsets and unchanged draft excerpts, with explicit
omissions. It is runtime feedback rather than source evidence; its candidate
hash binds it to the current draft. A clean numeric check does not establish
semantic correctness or task completion.

An unmatched candidate returns to the original human conversation and actual
authorized tool context, outside the answer-only rewrite schema. The model can
remove an unsupported optional claim or obtain the needed evidence/calculation;
the runtime does not choose a business source, edit the answer or waive a
numeric check. Required user work must remain covered. Numeric repair retains
its existing two-attempt limit independently of source recovery, and corrected
candidates still require numeric and semantic verification before delivery.

### Request-local identities at tool boundaries

Model-facing source handles and durable application IDs have different roles.
Built-in tools explicitly declare the ID fields they own; root-only selectors
are decoded only at their schema location. An identically named field inside
an opaque operation payload, SQL, a quotation or an external MCP schema does
not inherit that permission. Only a handle registered in the current request
can resolve. Resolution supplies an ID to the existing authorization check;
it never authorizes that ID or supplies evidence about the source contents.

Structured tools keep their result payloads opaque while decoding their own
knowledge-base selector. This makes source handles usable during a follow-up
evidence read without modifying dataset IDs, series IDs or business values.

### Compatible source spans across search and reads

Document search returns the complete indexed chunk body separately from its
match snippet. Its source metadata explicitly identifies offset zero in chunk
Unicode coordinates and a complete chunk body. Later reads can therefore
recognize the same source span. Missing coordinates remain unknown; different
bodies, source identities or explicit offsets still trigger ambiguity protection.
A complete chunk is not a complete document or a complete research task.

Source review logs only projection counts and omission/ambiguity/reader status,
without source text or draft excerpts. These diagnostics distinguish unavailable
projection evidence from a semantic verdict; they never supply facts or waive
verification.

### Retrieval coverage in both review contexts

The independent rewrite and final source reviewer consume the same canonical
current-turn execution payloads. Document read/search payloads include their
actual validated invocation arguments as execution-scope metadata; this metadata
is not a source statement or a new authorization. A separately bounded retrieval
companion includes returned chunk observations regardless of whether the draft
cited them. Exact body strings can share a storage entry while each observation
retains its real document/chunk identity, scope and immutable JSON pointer.

This gives the model access to uncited counterevidence when judging the requested
coverage. It does not rank sources by question text or decide which documents
are equivalent. Projection completeness covers only the returned result rows;
original pagination, truncation and scan ranges still apply. Omitted bodies or
metadata remain unknown. The critic can use its existing bounded evidence reader;
the rewrite context instead retains ordinary authorized source tools, and its
provenance handles do not imply reader access. Numeric, source and permission
validation continue to use the original execution data.

### Finding omitted evidence without guessing JSON paths

The existing review reader also offers exact literal search over the current
turn's already authorized successful result data. The reviewer chooses the
literal and interprets the returned windows; the runtime returns real JSON
pointers and Unicode offsets. This is recall evidence, not business intent
classification, document authority selection or semantic absence detection.
Search never retrieves another source. Its bounded scan work is accounted
separately from the unchanged four-call and 16,000-character response allowance;
partial scans and omitted matches remain explicit.

When a reviewer requests additional source collection without inspecting any
of its omitted results, the harness supplies one bounded reminder to inspect
existing evidence first. It does not force a business query or verdict, waive a
source check, or create another generator recovery budget. Acceptance, ordinary
answer revision and malformed verdict handling retain their existing paths.

### Citation attachment and review protocol boundaries

Source review also receives a separately bounded per-occurrence citation view.
It records actual markup positions, local answer navigation windows and pointers
to the exact already-fetched chunk evidence. Repeated uses stay distinct; another
retrieved chunk cannot silently support an incorrect adjacent citation. Identity
resolution is not semantic entailment. The model decides whether the requested
claim is supported, while runtime code validates identities and byte budgets.
Partial source ranges retain their limits without automatically invalidating a
supported local claim.

The exact review schema is present in the system protocol as well as the provider
format option, including multimodal calls whose providers offer JSON-object mode
rather than schema enforcement. A single Markdown JSON envelope or an isolated
closing fence on its own final line may be normalized as transport framing. The
remaining complete value must still satisfy all existing key, type, recovery,
length, natural-stop and EOF checks. Prose, multiple values, conflicting verdicts
and invalid recovery dispositions are not repaired into acceptance. Protocol
retries and semantic evidence-repair budgets remain separate and bounded.

### Recovering exact-quotation selection errors

Wiki source retraction retains exact-quotation uniqueness, source identity,
snapshot hashes and full listed-window coverage checks. A typed projection
validation failure can return up to three verbatim local match contexts to the
model in a 2 KiB diagnostic. The model repairs its selection for that same batch;
the program does not select a business interpretation or guess an occurrence.
At most two repairs use spare capacity inside the existing 32-call ceiling.
Failed attempts never commit quote identities, selected-text budgets or receipts.
Cancellation, transport errors, truncation and unexpected tool calls retain
their original failure paths. Exhausted repair still leaves retraction incomplete
and stale Wiki facts withheld from question-answering.

### Preserving citation identities across independent rewrites

The independent rewrite uses a runtime-owned JSON user envelope. Ordinary
assistant/tool replay encoding deliberately does not rewrite arbitrary user
JSON or source prose. Consequently that envelope needs an explicit boundary:
its draft citations are compacted as assistant replay before JSON serialization,
using the same request registry that later decodes the rewritten answer.

A separate 8 KiB handle directory joins actual retrieved chunk IDs and immutable
evidence pointers to their already citable request handles. History/navigation
registration cannot grant citation authority. Original source bodies, observation
IDs and JSON pointers remain unchanged; unavailable and omitted mappings are
explicit and must not be guessed. This is an identity protocol, not a source
ranking policy or a semantic judgment about which claim a chunk supports.

### 完整观测与裁剪视图的边界

引用句柄目录必须从本轮完整、成功且已授权的源观测构造，不能从已经裁剪的正文投影反推出全量目录。真实草稿引用优先保留同一请求注册表中的绑定，其余来源按稳定顺序展示；预算不足时明确报告真实省略数量。句柄存在只验证来源身份，不证明相邻主张获该来源支持，语义支持仍由模型结合真实正文核验。此约束不增加模型调用或正文预算。

Wiki 撤回的编辑重试须同时携带原页面、上一版完整候选及对应审核反馈。原页面不可变，候选作为转义后的不可信数据传入，引用继续使用同一轮真实句柄。模型保留仍受幸存来源支持的已有主张，判断反馈本身是否成立；幸存来源的完整材料不意味着必须把所有新事实补进旧页面。来源作者的范围与不确定性说明不同于代理执行旁白。来源权威必须有实际替代或优先关系证据，不能依到达顺序决定。原撤回校验、失败标记和调用预算继续适用。

Wiki 撤回审核的输出契约使用同一份 typed JSON Schema，同时交给模型请求的格式选项与显式提示。通过时 complete=true 且 issues=[]；拒绝时只列实际阻塞发现，issues 不承载分析过程或通过检查清单。示例实例不能冒充 schema，更不能同时演示通过和非空问题数组。服务端继续独立校验实际来源集合、协议字段、结束原因和反馈预算，不能通过检查反馈文字来猜测或改写审核判定。


Workbench 的内置默认入口可发现只读 Wiki 搜索与页面读取能力；是否实际注册仍由已授权知识库的索引能力和来源范围决定。自定义与共享代理继续遵循其配置，不通过工具发现扩权，也不加入 Wiki 写入能力。

Wiki 路由按不同的知识库所有者记录同一 slug。邻居链接登记与实际命中在同一知识库内只形成一个所有者，跨知识库的真实冲突继续显式保留。读取结果分别报告实际请求、解析得到的页面、真正渲染的页面身份、正文截断和整页省略；邻居路由表不代表已读取页面数量。撤回中的页面只能返回保护性通知，元数据须标明正文 withheld，不能把通知称为原文完整。

已审核答案的交付契约须同时出现在模型格式选项与可信系统说明中。JSON 字符串中的引号、换行和引用标记必须正确转义；程序不猜测或补造不完整引号。自然结束且不请求工具的非法交付可使用一次独立的序列化修复，失败响应仅作为 JSON 编码的不可信数据传给同一模型，禁用工具并保留原有输出上限与短超时。修复只产生候选，继续经过原有来源、任务完整性和确定性数值核验；不重置语义修复或证据恢复预算。额外调用用量单独计入本轮统计，不能把多次请求的总 prompt token 数当成单次上下文长度校准。

### Validate before revising

The workbench source critic assesses the exact generated candidate after
its deterministic numeric validation. Acceptance delivers that candidate;
a valid rejection enters the existing bounded revision or evidence-collection
path. An otherwise correct answer is not unconditionally restated before its
source review. The draft, runtime diagnostics and review advice remain separate
from successful tool evidence. Reviewer protocol errors and cancellation retain
their failure handling, and configurations without this source critic keep
their existing independent source audit.

Citation review uses shared dictionaries for actual source identities, source
scopes and immutable reader pointers. Compact occurrence rows reference those
identities rather than repeating metadata for each citation. Within unchanged
byte limits, source bodies receive Unicode-safe bounded segments with explicit
coverage and omission state. This is lossless identity/protocol compression,
not a semantic source selection rule or a claim that a partial body is complete.
The model still determines whether the adjacent claim is supported and whether
the remaining source needs to be reopened. No question text, domain label or
expected value controls this allocation.
