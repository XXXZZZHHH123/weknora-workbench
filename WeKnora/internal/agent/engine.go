package agent

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/agent/compaction"
	"github.com/Tencent/WeKnora/internal/agent/skills"
	agenttoken "github.com/Tencent/WeKnora/internal/agent/token"
	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/common"
	appconfig "github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/event"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/modelcontext"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/sandbox"
	"github.com/Tencent/WeKnora/internal/tracing/langfuse"
	"github.com/Tencent/WeKnora/internal/types"
)

// langfuseQueryPreview caps the query length we ship as the agent.execute
// span input — long quoted-context queries can be many KB of prose.
const langfuseQueryPreview = 2000

// AgentEngine is the core engine for running ReAct agents.
//
// History persistence note: the engine is stateless across turns. Conversation
// history is rebuilt from the DB once per turn by the caller
// (see service.LoadAgentHistory) and passed into Execute as llmContext. The
// engine therefore does not maintain its own cache, system-prompt store, or
// cross-turn buffer.
type AgentEngine struct {
	turnReview                       turnReviewState
	structuredReviewMode             bool           // bounded source review must not restart open-ended reasoning
	reviewedAnswerDeliveryRepairUsed bool           // at most one protocol-only serialization repair per turn
	answerReviewFeedback             string         // bounded semantic review advice, never source evidence
	answerReviewAction               string         // model-selected recovery disposition, never a source or tool command
	answerReviewPending              bool           // durable executed evidence requires approval before answer emission
	reviewConversation               []chat.Message // actual conversation messages for semantic review, excluding synthetic runtime envelopes
	reviewConversationReady          bool           // distinguishes an initialized empty turn context from a pre-turn fallback
	taskProjection                   *taskToolProjection
	taskPlanContext                  string
	taskPlan                         *taskEvidencePlan
	documentAnalysisSteers           []string // persisted human corrections only; never review envelopes
	config                           *types.AgentConfig
	toolRegistry                     *agenttools.ToolRegistry
	chatModel                        chat.Chat
	eventBus                         *event.EventBus
	knowledgeBasesInfo               []*KnowledgeBaseInfo    // Detailed knowledge base information for prompt
	selectedDocs                     []*SelectedDocumentInfo // User-selected documents (via @ mention)
	pinnedMCPServices                []*PinnedMCPServiceInfo // User @mentioned MCP services for this turn
	pinnedSkills                     []*PinnedSkillInfo      // User @mentioned skills for this turn
	questionOrigin                   *QuestionOriginInfo     // Source of a picked suggested question, if any
	sessionID                        string                  // Session ID for logging and event emission
	systemPromptTemplate             string                  // System prompt template (optional, uses default if empty)
	memoryPrompt                     string                  // Long-term memory envelope appended to the system prompt
	skillsManager                    *skills.Manager         // Skills manager for Progressive Disclosure (optional)
	appConfig                        *appconfig.Config       // Application config for prompt template resolution (optional)
	imageDescriber                   ImageDescriberFunc      // VLM function for describing images in tool results (optional)
	documentImageReviewer            chat.Chat               // Configured vision model for reviewing verified document images.
	imageReviewReasoning             chat.ReasoningEffort
	imageDescribeSlots               chan struct{}         // Bounds VLM calls across parallel tool calls
	tokenEstimator                   *agenttoken.Estimator // Token estimator for context window management, calibrated
	rawEstimator                     *agenttoken.Estimator // Same tokenizer at scale 1, for measuring the calibration
	compactor                        *compaction.Compactor // Summarizes older history to fit the context window (nil = disabled)
	lastUsage                        types.TokenUsage      // Token usage from the most recent LLM call
	lastSentMsgCount                 int                   // Number of messages sent in the most recent LLM call
	// overflowRecovered records that this turn already spent its one
	// compact-and-retry on a context overflow. A second attempt would mean
	// history size was never the problem, and looping on it burns the round
	// budget without changing anything.
	overflowRecovered bool
	// compactionExhaustedAt is the message count at which compaction last
	// reported it could free nothing. Below that count the answer has not
	// changed, so there is no reason to spend another summarization call.
	compactionExhaustedAt int
	// checkpointSink, when set, persists compactions that end on a stored
	// turn so later turns start from them. Nil keeps compaction turn-local.
	checkpointSink types.ContextCheckpointSink
	// calibration learns the provider's tokens per estimated token from
	// consecutive requests (see calibrateEstimator).
	calibration tokenCalibration
	// contextRewrites counts compactions and tool-result trims. Either one
	// changes messages an earlier request already sent, so the next request
	// cannot be compared with that one for calibration.
	contextRewrites int
	modelContext    *modelcontext.Registry // single request-local boundary for every model handle
	// steerSink, when set, lets users append messages into the running turn.
	// Drained at every round boundary; nil disables mid-run injection.
	steerSink          types.SteerSink
	allowSteerOverrun  bool // one extra ReAct round after a loop-end inject past MaxIterations
	steerOverruns      int  // how many times this turn has already used the extra round
	workspaceLayout    sandbox.WorkspaceLayout
	artifactFileSource agenttools.SandboxFileSource
}

// maxSteerOverruns caps loop-end injects past MaxIterations. One extra round
// lets a last-moment nudge revise the answer; further injects stay queued for
// a follow-up turn instead of stretching the same run indefinitely.
const maxSteerOverruns = 1

// ImageDescriberFunc generates a text description of an image.
// Signature matches vlm.VLM.Predict so it can be injected without importing the vlm package.
type ImageDescriberFunc func(ctx context.Context, imgBytes []byte, prompt string) (string, error)

// NewAgentEngine creates a new agent engine
func NewAgentEngine(
	config *types.AgentConfig,
	chatModel chat.Chat,
	toolRegistry *agenttools.ToolRegistry,
	eventBus *event.EventBus,
	knowledgeBasesInfo []*KnowledgeBaseInfo,
	selectedDocs []*SelectedDocumentInfo,
	sessionID string,
	systemPromptTemplate string,
) *AgentEngine {
	if eventBus == nil {
		eventBus = event.NewEventBus()
	}
	tokenEst, err := agenttoken.NewEstimator()
	if err != nil {
		return nil
	}
	// The previous turn's measured scale, so this turn's first compaction
	// check is calibrated before it has a provider count of its own.
	tokenEst.SetScale(config.ContextTokenScale)
	engine := &AgentEngine{
		config:               config,
		toolRegistry:         toolRegistry,
		chatModel:            chatModel,
		eventBus:             eventBus,
		knowledgeBasesInfo:   knowledgeBasesInfo,
		selectedDocs:         selectedDocs,
		sessionID:            sessionID,
		systemPromptTemplate: systemPromptTemplate,
		tokenEstimator:       tokenEst,
		rawEstimator:         tokenEst.Unscaled(),
		modelContext:         modelcontext.NewRegistry(config.CitationsEnabled()),
	}

	engine.compactor = compaction.New(chatModel, tokenEst, compaction.Settings{
		Enabled:          true,
		MaxContextTokens: config.MaxContextTokens,
		ReserveTokens:    engine.contextReserveTokens(),
		KeepRecentTokens: config.CompactionKeepRecentTokens,
		MaxSummaryTokens: engine.getCompletionTokenBudget(),
		StallTimeout:     engine.getLLMStallTimeout(),
	})

	return engine
}

// SetQuestionOrigin records the knowledge source of a suggested question the
// user picked for this turn; nil clears it.
func (e *AgentEngine) SetQuestionOrigin(origin *QuestionOriginInfo) {
	e.questionOrigin = origin
}

// SetPinnedMentions sets per-turn @mention scope for MCP services and skills.
func (e *AgentEngine) SetPinnedMentions(mcpServices []*PinnedMCPServiceInfo, skills []*PinnedSkillInfo) {
	e.pinnedMCPServices = mcpServices
	e.pinnedSkills = skills
}

// SetWorkspaceLayout injects the session's real workspace into the system
// prompt. Host sessions must pass the user directory so the model does not
// keep listing /workspace.
func (e *AgentEngine) SetWorkspaceLayout(layout sandbox.WorkspaceLayout) {
	if e == nil {
		return
	}
	e.workspaceLayout = layout
}

func (e *AgentEngine) systemPromptOptions(ctx context.Context) *BuildSystemPromptOptions {
	opts := &BuildSystemPromptOptions{
		Language:         types.LanguageNameFromContext(ctx),
		Config:           e.appConfig,
		SkillInstallMode: e.config.SkillInstallMode(),
		MemoryPrompt:     e.memoryPrompt,
		ProtocolPrompt:   e.modelContext.ProtocolPrompt(),
		WorkspaceLayout:  e.workspaceLayout,
	}
	if e.skillsManager != nil && e.skillsManager.IsEnabled() {
		opts.SkillsMetadata = e.skillsManager.GetAllMetadata()
	}
	if e.toolRegistry != nil {
		opts.SelectedTools = e.toolRegistry.ListTools()
		if _, err := e.toolRegistry.GetTool("local_browser"); err == nil {
			metadata := make([]*skills.SkillMetadata, 0, len(opts.SkillsMetadata))
			for _, item := range opts.SkillsMetadata {
				if item != nil && item.Name != "browser" && item.Name != "browser-skill" {
					metadata = append(metadata, item)
				}
			}
			opts.SkillsMetadata = metadata
		}
		_, err := e.toolRegistry.GetTool(agenttools.ToolShellExec)
		opts.ShellExecEnabled = err == nil
	}
	return opts
}

func (e *AgentEngine) buildSystemPrompt(ctx context.Context) string {
	sections := BuildSystemPromptSections(
		e.knowledgeBasesInfo,
		e.config.WebSearchEnabled,
		e.systemPromptOptions(ctx),
		e.systemPromptTemplate,
	)
	for _, section := range sections {
		logger.Debugf(ctx, "[Agent][Prompt] section=%s bytes=%d", section.Name, len(section.Content))
	}
	return renderSystemPromptSections(sections)
}

// SetMemoryPrompt supplies the long-term memory envelope for this run. Empty
// input leaves the system prompt untouched.
func (e *AgentEngine) SetMemoryPrompt(prompt string) {
	e.memoryPrompt = prompt
}

// NewAgentEngineWithSkills creates a new agent engine with skills support
func NewAgentEngineWithSkills(
	config *types.AgentConfig,
	chatModel chat.Chat,
	toolRegistry *agenttools.ToolRegistry,
	eventBus *event.EventBus,
	knowledgeBasesInfo []*KnowledgeBaseInfo,
	selectedDocs []*SelectedDocumentInfo,
	sessionID string,
	systemPromptTemplate string,
	skillsManager *skills.Manager,
) *AgentEngine {
	engine := NewAgentEngine(
		config,
		chatModel,
		toolRegistry,
		eventBus,
		knowledgeBasesInfo,
		selectedDocs,
		sessionID,
		systemPromptTemplate,
	)
	engine.skillsManager = skillsManager
	return engine
}

// SetAppConfig sets the application config for prompt template resolution.
// This allows the engine to read default prompts from config/prompt_templates/ YAML files.
func (e *AgentEngine) SetAppConfig(cfg *appconfig.Config) {
	e.appConfig = cfg
}

// SetImageDescriber sets the VLM function for generating text descriptions of images
// in tool results. When set and the chat model cannot view images, tool result
// images are described while their tool call runs and the descriptions are
// appended to the tool message content.
func (e *AgentEngine) SetImageDescriber(fn ImageDescriberFunc) {
	e.imageDescriber = fn
	e.imageDescribeSlots = make(chan struct{}, toolImageDescribeConcurrency)
}

// The primary answer model and its user-selected reasoning remain unchanged.
// This reviewer is eligible only after a document tool returns verified pixels.
func (e *AgentEngine) SetDocumentImageReviewModel(model chat.Chat, reasoning string) {
	e.documentImageReviewer = model
	e.imageReviewReasoning = chat.ReasoningEffort(reasoning)
	if e.imageReviewReasoning == "" {
		e.imageReviewReasoning = chat.ReasoningEffort("off")
	}
}

// describeToolImages runs inside the tool call, so the UI keeps showing the
// call as running while the vision model works and the description lands in
// the tool result the user sees.
func (e *AgentEngine) describeToolImages(ctx context.Context, result *types.ToolResult) {
	if result == nil || !result.Success || len(result.Images) == 0 || e.imageDescriber == nil ||
		(e.config != nil && e.config.ChatModelSupportsVision) {
		return
	}
	if len(result.ImageDescriptions) != len(result.Images) {
		result.ImageDescriptions = e.describeImages(ctx, result.Images, toolImagePrompts(result))
	}
	if result.Data != nil {
		result.Data["image_descriptions"] = result.ImageDescriptions
	}
}

// SetSkillsManager sets the skills manager for the engine
func (e *AgentEngine) SetSkillsManager(manager *skills.Manager) {
	e.skillsManager = manager
}

// GetSkillsManager returns the skills manager
func (e *AgentEngine) GetSkillsManager() *skills.Manager {
	return e.skillsManager
}

// estimateCurrentTokens returns the best estimate of the current context token
// count:
//
//   - When API usage from a previous round is available, that number is the
//     baseline (the provider already billed tools + system + history) and only
//     the messages appended since then are BPE-estimated.
//   - Otherwise it is a pure message-size estimate. Tool schemas are NOT
//     added here. Adding 232 tool schemas (~105k) to a 12k conversation made
//     every round look over the 111k threshold even after a successful
//     compaction.
//
// The baseline already contains the assistant reply, as the `output` half of
// the round that produced it. So the delta must start *after* that reply.
func (e *AgentEngine) estimateCurrentTokens(messages []chat.Message) int {
	if baseline := contextTokensFromUsage(e.lastUsage); baseline > 0 &&
		e.lastSentMsgCount > 0 && e.lastSentMsgCount <= len(messages) {
		return baseline + e.tokenEstimator.EstimateMessages(messages[e.deltaStart(messages):])
	}
	return e.tokenEstimator.EstimateMessages(messages)
}

// deltaStart is the first message not already accounted for by e.lastUsage.
func (e *AgentEngine) deltaStart(messages []chat.Message) int {
	start := e.lastSentMsgCount
	// The reply to the previous request lands here. Its tokens are the usage's
	// completion half, so skip it — but only if it is really there, since a
	// failed round appends nothing.
	if start < len(messages) && messages[start].Role == "assistant" {
		start++
	}
	return start
}

// contextTokensFromUsage reduces a usage report to the size of the context it
// describes. Cache counters are not added in: WeKnora normalizes PromptTokens
// to the provider's full input count, with read/write/miss as descriptive
// subsets of it (see types.TokenUsage.SetPromptCacheUsage).
func contextTokensFromUsage(usage types.TokenUsage) int {
	if usage.TotalTokens > 0 {
		return usage.TotalTokens
	}
	return usage.PromptTokens + usage.CompletionTokens
}

// Execute executes the agent with conversation history and streaming output
// All events are emitted to EventBus and handled by subscribers (like Handler layer)
func (e *AgentEngine) Execute(
	ctx context.Context,
	sessionID, messageID, query string,
	llmContext []chat.Message,
	imageURLs ...[]string,
) (*types.AgentState, error) {
	e.structuredReviewMode = false
	e.reviewedAnswerDeliveryRepairUsed = false
	e.answerReviewPending = false
	logger.Infof(ctx, "[Agent] Starting execution: session=%s, message=%s, query_len=%d, context_msgs=%d",
		sessionID, messageID, len(query), len(llmContext))
	ctx = withFieldConfirmationTurn(ctx, query, llmContext)
	// Ensure tools are cleaned up after execution
	defer e.toolRegistry.Cleanup(ctx)

	common.PipelineInfo(ctx, "Agent", "execute_start", map[string]interface{}{
		"session_id":   sessionID,
		"message_id":   messageID,
		"query":        query,
		"context_msgs": len(llmContext),
	})

	// Open a top-level Langfuse span so the agent run — including every
	// round's LLM call and every tool execution — groups under a single
	// node in the Langfuse UI instead of being flat children of the HTTP
	// trace. No-op when Langfuse is disabled.
	imgCount := 0
	if len(imageURLs) > 0 {
		imgCount = len(imageURLs[0])
	}
	kbIDs := make([]string, 0, len(e.knowledgeBasesInfo))
	for _, kb := range e.knowledgeBasesInfo {
		if kb != nil {
			kbIDs = append(kbIDs, kb.ID)
		}
	}
	spanCtx, agentSpan := langfuse.GetManager().StartSpan(ctx, langfuse.SpanOptions{
		Name: "agent.execute",
		Input: map[string]interface{}{
			"query":        truncateRunes(query, langfuseQueryPreview),
			"query_len":    len(query),
			"context_msgs": len(llmContext),
			"image_count":  imgCount,
		},
		Metadata: map[string]interface{}{
			"session_id":          sessionID,
			"message_id":          messageID,
			"max_iterations":      e.config.MaxIterations,
			"parallel_tool_calls": e.config.ParallelToolCalls,
			"web_search":          e.config.WebSearchEnabled,
			"multi_turn":          e.config.MultiTurnEnabled,
			"knowledge_base_ids":  kbIDs,
			"allowed_tools":       e.config.AllowedTools,
		},
	})
	ctx = spanCtx

	// Initialize state
	state := &types.AgentState{
		RoundSteps:    []types.AgentStep{},
		KnowledgeRefs: []*types.SearchResult{},
		IsComplete:    false,
		CurrentRound:  0,
		// A turn that measures no scale of its own (no tool call, so no two
		// requests to compare) passes on the one it started from, so the
		// newest turn always carries the latest known scale and the loader
		// never has to look past its first page for one.
		TurnUsage: types.TokenUsage{ContextTokenScale: e.config.ContextTokenScale},
	}

	if e.config.WorkbenchDefault && e.toolRegistry != nil {
		e.toolRegistry.RegisterTool(newSourceCalculationTool(func() []types.AgentStep { return state.RoundSteps }))
		e.toolRegistry.RegisterTool(newDocumentTableCalculationTool(func() []types.AgentStep { return state.RoundSteps }))
		e.registerDocumentTableAnalysis(state, query, sessionID, llmContext)
	}
	e.prepareTaskPlanning()

	// Build system prompt using progressive RAG prompt
	// If skills are enabled, include skills metadata (Level 1 - Progressive Disclosure)
	systemPrompt := e.buildSystemPrompt(ctx)
	if e.config.WorkbenchDefault {
		systemPrompt += "\n" + researchSynthesisInstructions + "\n" + researchTrendInstructions + "\n" + researchDeliveryInstructions
	}
	if e.toolRegistry.HasStructuredDataTools() {
		if !e.config.WorkbenchDefault {
			systemPrompt += structuredFieldTurnInstructions
		}
		systemPrompt += structuredLatencyInstructions
	}
	if e.config.WorkbenchDefault {
		systemPrompt += "\n" + workbenchTaskScopeInstructions
		systemPrompt += "\n" + documentVisualReadInstructions
	}
	logger.Debugf(ctx, "[Agent] SystemPrompt: %d chars", len(systemPrompt))

	// Initialize messages with history
	var imgs []string
	if len(imageURLs) > 0 {
		imgs = imageURLs[0]
	}
	messages := e.buildMessagesWithLLMContext(systemPrompt, query, sessionID, llmContext, imgs)
	if e.toolRegistry != nil {
		e.toolRegistry.RememberMCPHistory(messages)
		e.toolRegistry.RefreshMCPTools(ctx)
	}

	if !strings.HasPrefix(query, fieldConfirmationPrefix) {
		messages = e.planWorkbenchTask(ctx, state, query, messages, sessionID)
		messages = e.discoverWorkbenchSources(ctx, state, messages, query, sessionID, messageID, e.plannedSourceDiscoverySelection())
	}

	// Get tool definitions for function calling
	tools := e.buildToolsForLLM()
	toolListStr := strings.Join(listToolNames(tools), ", ")
	logger.Infof(ctx, "[Agent] Ready: %d messages, %d tools [%s], mcp_catalog=%d chars, %d images",
		len(messages), len(tools), toolListStr, mcpCatalogDescriptionLen(tools), len(imgs))
	common.PipelineInfo(ctx, "Agent", "tools_ready", map[string]interface{}{
		"session_id": sessionID,
		"tool_count": len(tools),
		"tools":      toolListStr,
	})

	if e.executeStructuredConfirmation(ctx, state, query, sessionID, messageID, &messages) {
		finishAgentSpan(agentSpan, state, nil)
		return state, nil
	}
	_, err := e.executeLoop(ctx, state, query, messages, tools, sessionID, messageID)
	if err != nil {
		logger.Errorf(ctx, "[Agent] Execution failed: %v", err)
		e.eventBus.Emit(ctx, event.Event{
			ID:        generateEventID("error"),
			Type:      event.EventError,
			SessionID: sessionID,
			Data: event.ErrorData{
				Error:     err.Error(),
				Stage:     "agent_execution",
				SessionID: sessionID,
			},
		})
		finishAgentSpan(agentSpan, state, err)
		return nil, err
	}

	logger.Infof(ctx, "[Agent] Completed: %d rounds, %d steps, complete=%v",
		state.CurrentRound, len(state.RoundSteps), state.IsComplete)
	common.PipelineInfo(ctx, "Agent", "execute_complete", map[string]interface{}{
		"session_id": sessionID,
		"rounds":     state.CurrentRound,
		"steps":      len(state.RoundSteps),
		"complete":   state.IsComplete,
	})
	finishAgentSpan(agentSpan, state, nil)
	return state, nil
}

// finishAgentSpan records the final outcome of an agent execution onto the
// top-level Langfuse span. Extracted so the same payload is used for both
// success and error return paths in Execute().
func finishAgentSpan(span *langfuse.Span, state *types.AgentState, err error) {
	if span == nil {
		return
	}
	totalToolCalls := 0
	for _, step := range state.RoundSteps {
		totalToolCalls += len(step.ToolCalls)
	}
	output := map[string]interface{}{
		"rounds":           state.CurrentRound,
		"steps":            len(state.RoundSteps),
		"tool_calls":       totalToolCalls,
		"complete":         state.IsComplete,
		"final_answer_len": len(state.FinalAnswer),
		"final_answer":     truncateRunes(state.FinalAnswer, langfuseQueryPreview),
	}
	span.Finish(output, map[string]interface{}{
		"rounds":     state.CurrentRound,
		"steps":      len(state.RoundSteps),
		"tool_calls": totalToolCalls,
		"complete":   state.IsComplete,
	}, err)
}

// truncateRunes caps s to n runes and appends "…" when truncated. Identical
// in spirit to the helper in act.go, but kept locally so both files stay
// independent and the truncation budget can diverge if needed.
func truncateRunes(s string, n int) string {
	if n <= 0 || s == "" {
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// withinIterationBudget reports whether another ReAct round is allowed.
// A negative MaxIterations is unlimited: the loop still stops on a natural
// finish, user cancel, empty-response retries, or a repeated-content stall.
func (e *AgentEngine) withinIterationBudget(round int) bool {
	if e.config == nil {
		return false
	}
	if e.config.UnlimitedIterations() {
		return true
	}
	return round < e.config.MaxIterations
}

// loopGuards is the per-turn state the ReAct loop uses to notice it is not
// making progress. All three counters exist because a model can keep the loop
// spinning without ever finishing: empty answers, the same answer repeated,
// and rounds cut off at the completion-token cap.
type loopGuards struct {
	incompleteAnswerRetries   int
	evidenceRecoveryAttempts  int
	structuredAnswerReviewed  bool
	structuredEvidenceRepairs int
	numericValidationInputs   []string
	structuredReviewEvidence  []chat.Message
	structuredReviewInput     string
	// emptyRetries counts nudges sent after a stop with no content.
	emptyRetries int
	// consecutiveSameContent counts plain rounds that repeated the previous
	// plain round verbatim, an identical lack of content included.
	consecutiveSameContent int
	// sawPlainRound records whether lastResponseContent holds a real
	// observation yet, so the first plain round of a turn is never counted as
	// a repeat of the zero value.
	sawPlainRound       bool
	lastResponseContent string
	// consecutiveLength counts rounds in a row that the provider cut off at
	// the completion-token cap.
	consecutiveLength int
}

// closeAnswerStream emits the Done:true marker for an answer that is actually
// finishing. Loop-end inject skips this so the client does not drop isReplying
// while the engine continues. truncated rides along on the marker because an
// answer streamed live only finds out about the completion cap at the close.
func (e *AgentEngine) closeAnswerStream(ctx context.Context, sessionID, answerID string, truncated bool) {
	if e.eventBus == nil || answerID == "" || ctx.Err() != nil || e.turnReview.ClosedStreams[answerID] {
		return
	}
	if e.turnReview.ClosedStreams == nil {
		e.turnReview.ClosedStreams = make(map[string]bool)
	}
	e.turnReview.ClosedStreams[answerID] = true
	_ = e.eventBus.Emit(ctx, event.Event{
		ID:        answerID,
		Type:      event.EventAgentFinalAnswer,
		SessionID: sessionID,
		Data: event.AgentFinalAnswerData{
			Content:   "",
			Done:      true,
			Truncated: truncated,
		},
	})
}

// finishStalledTurn ends a turn that a guard stopped rather than the model.
// Nonempty suspicious short output is checked against the actual requested
// format. Missing or unverified output receives an explicit runtime fallback.
// The stream is closed here because a stopped loop cannot reach natural stop.
func (e *AgentEngine) finishStalledTurn(
	ctx context.Context,
	state *types.AgentState,
	sessionID string,
	response *types.ChatResponse,
	truncated bool,
	query string,
	conversation []chat.Message,
	runtimeFallback ...bool,
) {
	e.finishStalledTurnWithGuards(ctx, state, sessionID, response, truncated, query, conversation, nil, runtimeFallback...)
}

func (e *AgentEngine) finishStalledTurnWithGuards(
	ctx context.Context,
	state *types.AgentState,
	sessionID string,
	response *types.ChatResponse,
	truncated bool,
	query string,
	conversation []chat.Message,
	guards *loopGuards,
	runtimeFallback ...bool,
) {
	conversation = restoreForFinalAnswer(conversation, guards)
	// Text that arrived alongside tool calls is a preamble — "let me look that
	// up" — from a round that meant to keep working. Presenting it as the final
	// answer would show an opening line and call the turn finished, so the
	// fallback speaks for it instead.
	answer := response.Content
	answerStreamed := response.AnswerStreamed
	if len(response.ToolCalls) > 0 {
		answer = ""
	}
	if !(len(runtimeFallback) > 0 && runtimeFallback[0]) && strings.TrimSpace(answer) != "" {
		decision := e.checkCandidate(ctx, query, answer, response.ReasoningContent, state, conversation, utf8.RuneCountInString(strings.TrimSpace(answer)) <= 4)
		if ctx.Err() != nil {
			return
		}
		if decision.Err != nil {
			answer = answerReviewUnavailableFallback
			answerStreamed = false
		} else if decision.Disposition != candidateAccepted {
			answer = ""
		}
	}

	answerID := ""
	if answerStreamed && answer != "" {
		answerID = response.AnswerEventID
	}
	if strings.TrimSpace(answer) == "" {
		answer = stalledAnswerFallback
		if truncated {
			answer = truncatedAnswerFallback
		}
		answerID = ""
	}
	e.commitTerminalResult(ctx, state, sessionID, answer, answerID, "stalled", truncated)
}

func (e *AgentEngine) maxIterationsDisplay() string {
	if e.config != nil && e.config.UnlimitedIterations() {
		return "unlimited"
	}
	n := 0
	if e.config != nil {
		n = e.config.MaxIterations
	}
	return fmt.Sprintf("%d", n)
}

// executeLoop executes the main ReAct loop
// All events are emitted through EventBus with the given sessionID
func (e *AgentEngine) executeLoop(
	ctx context.Context,
	state *types.AgentState,
	query string,
	messages []chat.Message,
	tools []chat.Tool,
	sessionID string,
	messageID string,
) (*types.AgentState, error) {
	// Keep a turn-local snapshot of real conversation through the last human
	// request. Later assistant plans, tool exchanges and synthetic reviewer
	// envelopes belong to the runtime transcript, not the semantic history.
	e.turnReview = turnReviewState{}
	e.reviewConversation = conversationThroughLastHuman(messages)
	e.reviewConversationReady = true
	defer func() {
		e.reviewConversation = nil
		e.reviewConversationReady = false
	}()

	startTime := time.Now()
	common.PipelineInfo(ctx, "Agent", "loop_start", map[string]interface{}{
		"max_iterations": e.config.MaxIterations,
	})

	// Guarantee exactly-one EventAgentComplete emission on every exit path
	// (normal finish, ctx cancel observed at the loop head, or iteration error
	// bubbling up while ctx was cancelled). The emission triggers
	// agent_stream_handler.handleComplete, which is where state.RoundSteps
	// (thinking + tool_call history) gets written onto assistantMessage.AgentSteps
	// so it can be persisted by the caller's defer. Use WithoutCancel so a
	// cancelled ctx does not short-circuit the emit on the stop path.
	completionEmitted := false
	emitCompletion := func() {
		if completionEmitted {
			return
		}
		completionEmitted = true
		e.emitCompletionEvent(context.WithoutCancel(ctx), state, sessionID, messageID, startTime)
	}
	defer emitCompletion()

	guards := &loopGuards{}
loop:
	for e.withinIterationBudget(state.CurrentRound) || e.allowSteerOverrun {
		e.allowSteerOverrun = false
		// Check for context cancellation (request timeout, user cancel, etc.)
		select {
		case <-ctx.Done():
			logger.Warnf(ctx, "[Agent] Context cancelled at round %d: %v",
				state.CurrentRound+1, ctx.Err())
			// Cancellation never starts another synthesis or marks a draft complete.
			return state, ctx.Err()
		default:
		}

		// A slow startup, OAuth discovery or explicit refresh may have produced
		// new definitions since the previous response. Publish them only here,
		// after all previous tool calls have finished, and rebuild the wire list.
		if e.toolRegistry != nil {
			e.toolRegistry.RefreshMCPTools(ctx)
			tools = e.buildToolsForLLM()
		}

		// Each iteration runs inside an "agent.round.<N>" Langfuse span.
		// We execute the body in a closure so `defer span.Finish()` fires at
		// every exit path (break/continue/next) without having to sprinkle
		// manual finish calls throughout the many branches below.
		outcome, iterErr := e.runReActIteration(ctx, state, &messages, tools,
			sessionID, messageID, query, guards)
		if iterErr != nil {
			return state, iterErr
		}
		switch outcome {
		case iterOutcomeContinue:
			continue loop
		case iterOutcomeBreak:
			break loop
		case iterOutcomeNext:
			state.CurrentRound++
		}
	}

	// If loop finished without final answer, generate one — but skip this
	// when the context was cancelled (user pressed stop). In that case the
	// fallback LLM call would fail on the already-cancelled ctx and set
	// state.FinalAnswer to the generic "Sorry, I was unable to generate a
	// complete answer." message, which then leaks to the UI as the final
	// answer for a conversation the user deliberately stopped.
	if !state.IsComplete && ctx.Err() == nil {
		finalMessages := restoreForFinalAnswer(messages, guards)
		e.handleMaxIterations(ctx, query, state, sessionID, finalMessages)
	}

	return state, nil
}

// semanticReviewConversation returns the real turn-local conversation when
// executeLoop has initialized it, otherwise retaining compatibility for
// reviewers called outside a running turn. Returned slices are independent.
func (e *AgentEngine) semanticReviewConversation(fallback []chat.Message) []chat.Message {
	if e != nil && e.reviewConversationReady {
		return append([]chat.Message(nil), e.reviewConversation...)
	}
	return append([]chat.Message(nil), fallback...)
}

func conversationThroughLastHuman(messages []chat.Message) []chat.Message {
	lastHuman := -1
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" && messages[i].Kind != chat.MessageKindReviewEvidence {
			lastHuman = i
			break
		}
	}
	if lastHuman < 0 {
		return []chat.Message{}
	}
	return append([]chat.Message(nil), messages[:lastHuman+1]...)
}

// restoreForFinalAnswer removes only the runtime-owned reviewer envelope
// before finalization. The recovery helper preserves all actual live tail
// messages, including tool pairs and persisted steer messages.
func restoreForFinalAnswer(messages []chat.Message, guards *loopGuards) []chat.Message {
	restored, _ := restoreStructuredReviewConversation(messages, guards)
	return restored
}

// iterOutcome directs executeLoop's control flow after one ReAct iteration.
// Using a sentinel (rather than bare return values from runReActIteration)
// keeps the loop's break/continue/next branches explicit in one place.
type iterOutcome int

const (
	// iterOutcomeNext advances state.CurrentRound and loops again.
	iterOutcomeNext iterOutcome = iota
	// iterOutcomeContinue re-runs the loop without advancing the round
	// counter. Used by the empty-content retry path.
	iterOutcomeContinue
	// iterOutcomeBreak exits the loop (final answer, stuck loop, or end).
	iterOutcomeBreak
)

// runReActIteration executes one ReAct step: think → analyze → act → observe.
// Extracted from executeLoop so the whole iteration body can live inside a
// single `defer span.Finish()` scope — otherwise we'd need to sprinkle
// manual finish calls across every break/continue/return branch.
//
// The mutable loop state (messages and the no-progress guards) is shared
// across iterations so each round can see what the previous ones did.
func (e *AgentEngine) runReActIteration(
	parentCtx context.Context,
	state *types.AgentState,
	messagesPtr *[]chat.Message,
	tools []chat.Tool,
	sessionID, assistantMessageID, query string,
	guards *loopGuards,
) (outcome iterOutcome, retErr error) {
	roundStart := time.Now()
	round := state.CurrentRound + 1

	// Open the round-level Langfuse span. Any chat/tool calls made inside
	// this iteration will attach under it via ctx, giving the UI a clean
	// trace → agent.execute → agent.round.N → (chat + tools) structure.
	ctx, roundSpan := langfuse.GetManager().StartSpan(parentCtx, langfuse.SpanOptions{
		Name: fmt.Sprintf("agent.round.%d", round),
		Input: map[string]interface{}{
			"round":          round,
			"message_count":  len(*messagesPtr),
			"max_iterations": e.config.MaxIterations,
		},
		Metadata: map[string]interface{}{
			"iteration":  state.CurrentRound,
			"round":      round,
			"session_id": sessionID,
		},
	})

	var (
		response      *types.ChatResponse
		toolCallCount int
	)
	defer func() {
		if roundSpan == nil {
			return
		}
		out := map[string]interface{}{
			"round":      round,
			"outcome":    outcome.String(),
			"tool_calls": toolCallCount,
		}
		if response != nil {
			out["has_tool_calls"] = len(response.ToolCalls) > 0
			out["finish_reason"] = response.FinishReason
			out["content_len"] = len(response.Content)
			if response.Usage.TotalTokens > 0 {
				out["prompt_tokens"] = response.Usage.PromptTokens
				out["completion_tokens"] = response.Usage.CompletionTokens
				out["total_tokens"] = response.Usage.TotalTokens
			}
		}
		out["duration_ms"] = time.Since(roundStart).Milliseconds()
		roundSpan.Finish(out, map[string]interface{}{
			"round":       round,
			"tool_calls":  toolCallCount,
			"outcome":     outcome.String(),
			"duration_ms": time.Since(roundStart).Milliseconds(),
		}, retErr)
	}()

	// Compact older history before the next assistant response when the
	// estimate is over the threshold. The trigger is estimateCurrentTokens —
	// usage+delta when a previous round reported one, otherwise the message
	// list alone. Tool schemas are not added here.
	currentTokens := e.estimateCurrentTokens(*messagesPtr)
	managed, changed := e.manageContextWindow(ctx, *messagesPtr, round, currentTokens)
	if changed {
		*messagesPtr = managed
		currentTokens = e.tokenEstimator.EstimateMessages(managed)
	}

	// Mid-run steering: drain any user messages queued while the previous
	// round was thinking/executing tools. Runs after compression (injected
	// text stays inside the protected tail) and before lastSentMsgCount is
	// updated (the injected text counts as new delta tokens for the next
	// call), so the very next LLM call sees the user's addition.
	if e.drainSteerMessages(ctx, state, messagesPtr, sessionID, assistantMessageID) > 0 {
		e.clearTaskToolProjection(messagesPtr)
		e.taskPlan = nil // The user supplied new requirements.
		tools = e.buildToolsForLLM()
	}

	logger.Infof(ctx, "[Agent][Round-%d/%s] Starting: %d messages, %d tools, est_tokens=%d",
		round, e.maxIterationsDisplay(), len(*messagesPtr), len(tools), currentTokens)
	e.logContextPrediction(ctx, round, *messagesPtr, tools, currentTokens)
	common.PipelineInfo(ctx, "Agent", "round_start", map[string]interface{}{
		"iteration":      state.CurrentRound,
		"round":          round,
		"message_count":  len(*messagesPtr),
		"pending_tools":  len(tools),
		"max_iterations": e.config.MaxIterations,
	})

	// 1. Think: Call LLM with function calling (includes retry + graceful degradation)
	stage := "generation"
	if e.structuredReviewMode {
		stage = "verification"
	}
	e.emitProgress(ctx, stage, state.CurrentRound, sessionID)
	e.lastSentMsgCount = len(*messagesPtr)
	e.answerReviewPending = hasStructuredEvidence(state.RoundSteps) || hasCurrentDocumentEvidence(state.RoundSteps) || e.needsSourceConsistencyCheck(state) || hasStructuredHistoryEvidence(*messagesPtr)
	resp, err := e.callLLMWithRetry(ctx, messagesPtr, tools, state, query, state.CurrentRound, sessionID)
	if err != nil {
		retErr = err
		return iterOutcomeNext, err
	}
	if resp == nil {
		return iterOutcomeBreak, nil
	}
	accumulateAuxiliaryUsage(state, resp)

	// The round's own token estimate can be wrong — history is estimated, not
	// counted — so a request that looked safe can still come back having hit
	// the window. Compacting and retrying once turns that into a recovered
	// round instead of a wasted one. Once per turn: if the retry overflows
	// too, the problem is not the history size.
	if !e.overflowRecovered && e.responseHitContextLimit(resp) {
		e.overflowRecovered = true
		// This successful response is discarded when context recovery retries,
		// but its provider-reported primary usage is still part of the turn.
		// Keep it out of calibration: calibration below uses only the final
		// response's single-call prompt count.
		if usageHasCounts(resp.Usage) {
			state.TurnUsage.Accumulate(resp.Usage)
		}
		logger.Warnf(ctx, "[Agent][Round-%d] Response hit the context window (finish=%s, "+
			"completion=%d of %d requested); compacting and retrying once",
			round, resp.FinishReason, resp.Usage.CompletionTokens, e.getCompletionTokenBudget())
		*messagesPtr = e.forceCompaction(ctx, *messagesPtr, round)
		e.lastSentMsgCount = len(*messagesPtr)
		resp, err = e.callLLMWithRetry(ctx, messagesPtr, tools, state, query, state.CurrentRound, sessionID)
		if err != nil {
			retErr = err
			return iterOutcomeNext, err
		}
		if resp == nil {
			return iterOutcomeBreak, nil
		}
		accumulateAuxiliaryUsage(state, resp)
	}
	response = resp
	e.logContextDrift(ctx, round, currentTokens, response.Usage)
	// Calibration needs only a prompt count; some providers report no total.
	e.calibrateEstimator(ctx, round, *messagesPtr, tools, response.Usage, state)
	if response.Usage.TotalTokens > 0 {
		e.lastUsage = response.Usage
		state.TurnUsage.Accumulate(response.Usage)
		logger.Infof(ctx, "[Agent][Round-%d] Usage: prompt=%d, completion=%d, total=%d, "+
			"cache_read=%d, cache_write=%d, cache_hit_rate=%.1f%%, cache_status=%s",
			round, response.Usage.PromptTokens,
			response.Usage.CompletionTokens, response.Usage.TotalTokens,
			response.Usage.CacheReadTokens, response.Usage.CacheWriteTokens,
			response.Usage.PromptCacheHitRate(), response.Usage.CacheStatus)
	}

	// Every round in a row that the provider cut off at the completion cap.
	// A truncated *answer* already ends the turn in analyzeResponse, so a run
	// of these means the truncation keeps landing inside tool-call arguments:
	// act.go refuses the calls, the model is told to re-issue them, writes an
	// even longer call, and hits the cap again. Give up rather than spend the
	// whole round budget on it (#3446).
	if isLengthFinishReason(response.FinishReason) {
		guards.consecutiveLength++
		if guards.consecutiveLength >= maxConsecutiveLengthRounds {
			logger.Warnf(ctx, "[Agent][Round-%d] %d consecutive rounds cut off at the completion cap "+
				"(finish=%s, completion=%d of %d); stopping",
				round, guards.consecutiveLength, response.FinishReason,
				response.Usage.CompletionTokens, e.getCompletionTokenBudget())
			common.PipelineWarn(ctx, "Agent", "consecutive_length_stop", map[string]interface{}{
				"iteration":          state.CurrentRound,
				"round":              round,
				"consecutive_length": guards.consecutiveLength,
			})
			// Record the round the way every other round is recorded. The
			// refused tool calls are also what tells the UI that this round's
			// plain text was a preamble: without them the "let me look that
			// up" line stays sitting in the answer area, exactly as if it were
			// the answer.
			step := types.AgentStep{
				Iteration:        state.CurrentRound,
				Thought:          response.Content,
				ReasoningContent: response.ReasoningContent,
				ToolCalls:        make([]types.ToolCall, 0),
				Timestamp:        time.Now(),
				Truncated:        true,
			}
			if len(response.ToolCalls) > 0 {
				e.failTruncatedToolCalls(ctx, response, &step, state.CurrentRound, sessionID)
			}
			state.RoundSteps = append(state.RoundSteps, step)
			e.finishStalledTurnWithGuards(ctx, state, sessionID, response, true, query, *messagesPtr, guards)
			return iterOutcomeBreak, nil
		}
	} else {
		guards.consecutiveLength = 0
	}

	// Detect stuck loops: a round with no tool calls that repeats the previous
	// such round is not making progress. An identical *lack* of content counts
	// — a model burning its whole budget on reasoning returns empty every
	// round, which used to land in the else branch and reset the counter, so
	// the guard could never fire on the one shape that needs it most (#3446).
	if len(response.ToolCalls) == 0 {
		if guards.sawPlainRound && response.Content == guards.lastResponseContent {
			guards.consecutiveSameContent++
		} else {
			guards.consecutiveSameContent = 0
		}
		guards.sawPlainRound = true
		guards.lastResponseContent = response.Content
		if guards.consecutiveSameContent >= maxRepeatedResponseRounds {
			logger.Warnf(ctx, "[Agent][Round-%d] Detected stuck loop: same content repeated %d times (finish=%s), stopping",
				round, guards.consecutiveSameContent+1, response.FinishReason)
			e.finishStalledTurnWithGuards(ctx, state, sessionID, response, false, query, *messagesPtr, guards)
			return iterOutcomeBreak, nil
		}
	} else {
		guards.consecutiveSameContent = 0
		guards.sawPlainRound = false
		guards.lastResponseContent = ""
	}

	// Create agent step
	step := types.AgentStep{
		UserMessagesBefore: state.PendingSteerMessages,
		Iteration:          state.CurrentRound,
		Thought:            response.Content,
		ReasoningContent:   response.ReasoningContent,
		ReasoningSignature: response.ReasoningSignature,
		ReasoningMetadata:  response.ReasoningMetadata,
		ToolCalls:          make([]types.ToolCall, 0),
		Timestamp:          time.Now(),
	}
	state.PendingSteerMessages = nil

	// If the request was cancelled while the LLM was streaming (e.g. the
	// user pressed "stop"), the stream driver still returns a usable
	// response (partial content / finish_reason="stop" / no tool calls).
	// Do NOT let analyzeResponse treat that partial thinking text as the
	// final answer — it would pollute Message.Content with mid-stream
	// thinking and show up as a duplicate card next to the intermediate-
	// steps tree. Preserve the partial thinking as an AgentStep and break
	// out of the loop without marking state.IsComplete. executeLoop's
	// deferred emitCompletionEvent will persist the step onto
	// Message.AgentSteps so it still appears in the tree on refresh.
	if ctx.Err() != nil {
		logger.Warnf(ctx, "[Agent][Round-%d] Context cancelled during LLM call; preserving partial step",
			round)
		if step.Thought != "" || len(step.ToolCalls) > 0 || len(step.UserMessagesBefore) > 0 {
			state.RoundSteps = append(state.RoundSteps, step)
		}
		return iterOutcomeBreak, nil
	}

	// A normal stop is not proof that the provider delivered an answer. The
	// initial stream prefix is buffered so these fragments never become a
	// completed answer in the UI. Repair in the same evidence context, with
	// reasoning switched off for this turn only; never change model settings.
	if len(response.ToolCalls) == 0 && hasWorkspaceExecution(state) {
		e.recordArtifactInventory(ctx, state, messagesPtr, sessionID)
	}
	needsAnswerRepair := false
	runtimeAnswerFailure := response.FinishReason == "content_filter"
	// Agents without the workbench source critic retain their existing
	// independent source-audit path; direct acceptance requires that critic.
	pendingLegacySourceRewrite := !e.needsSourceConsistencyCheck(state) && len(response.ToolCalls) == 0 && response.Content != "" && !guards.structuredAnswerReviewed && (needsStructuredAnswerReviewForQuery(query, state.RoundSteps, *messagesPtr, response.Content) || hasCurrentDocumentEvidence(state.RoundSteps))
	if len(response.ToolCalls) == 0 && (!pendingLegacySourceRewrite || suspectAnswerFragment(response.Content, response.ReasoningContent)) && (isNaturalStopFinishReason(response.FinishReason) || isLengthFinishReason(response.FinishReason)) {
		reviewConversation, _ := restoreStructuredReviewConversation(*messagesPtr, guards)
		decision := e.checkCandidate(ctx, query, response.Content, response.ReasoningContent, state, reviewConversation, false)
		if ctx.Err() != nil {
			state.RoundSteps = append(state.RoundSteps, step)
			return iterOutcomeBreak, nil
		}
		if decision.Disposition == candidateNumericFailure {
			if guards.structuredEvidenceRepairs < 2 && e.spendCandidateRepair() {
				guards.structuredEvidenceRepairs++
				e.resumeNumericValidationRepair(ctx, state, messagesPtr, guards, sessionID, response.Content, decision.NumericFeedback)
				state.RoundSteps = append(state.RoundSteps, step)
				return iterOutcomeNext, nil
			}
			response.Content = "已经取得原表依据，但完整回答的部分数字未通过来源核验，暂不提供未经核验的统计结论。请查看取数依据。"
			response.AnswerStreamed = false
			response.AnswerEventID = ""
			pendingLegacySourceRewrite = false
			runtimeAnswerFailure = true
		} else if decision.Err != nil {
			logger.Warnf(ctx, "[Agent][Round-%d] Answer check failed: %v", round, decision.Err)
			state.RoundSteps = append(state.RoundSteps, step)
			fallback := *response
			fallback.Content = answerReviewUnavailableFallback
			fallback.AnswerStreamed = false
			fallback.AnswerEventID = ""
			e.finishStalledTurnWithGuards(ctx, state, sessionID, &fallback, false, query, *messagesPtr, guards, true)
			return iterOutcomeBreak, nil
		} else {
			needsAnswerRepair = decision.Disposition != candidateAccepted
		}
	}

	if needsAnswerRepair {
		state.RoundSteps = append(state.RoundSteps, step)
		if e.answerReviewAction == reviewCollectEvidence && guards.evidenceRecoveryAttempts < maxEvidenceRecoveryAttempts {
			e.resumeEvidenceCollection(ctx, state, messagesPtr, guards, sessionID)
			return iterOutcomeNext, nil
		}
		guards.incompleteAnswerRetries++
		if guards.incompleteAnswerRetries <= 2 && e.spendCandidateRepair() {
			if guards.structuredAnswerReviewed || needsStructuredAnswerReviewForQuery(query, state.RoundSteps, *messagesPtr, response.Content) || hasCurrentDocumentEvidence(state.RoundSteps) {
				e.beginCandidateRevision(ctx, state, messagesPtr, guards, query, response.Content)
			}
			e.structuredReviewMode = true
			guards.sawPlainRound = false
			guards.consecutiveSameContent = 0
			*messagesPtr = append(*messagesPtr, chat.Message{Role: "system", Content: "内部运行时反馈：上一轮答复的完整性未能确认。请依据本轮真实工具结果，按用户要求的语言和格式完成原问题；用户要求短答或特定字符时保持该格式。不要继续长篇推理，不要把草稿当成事实；证据缺失时明确说明缺哪项，不得编造数值。" + e.answerRepairAdvice()})
			return iterOutcomeContinue, nil
		}
		fallback := *response
		fallback.Content = incompleteAnswerFallback
		fallback.AnswerStreamed = false
		fallback.AnswerEventID = ""
		e.finishStalledTurnWithGuards(ctx, state, sessionID, &fallback, false, query, *messagesPtr, guards, true)
		return iterOutcomeBreak, nil
	}

	if pendingLegacySourceRewrite {
		e.beginCandidateRevision(ctx, state, messagesPtr, guards, query, response.Content)
		state.RoundSteps = append(state.RoundSteps, step)
		return iterOutcomeNext, nil
	}
	// 2. Analyze: Check for stop conditions (natural stop with no tool calls)
	verdict := e.analyzeResponse(ctx, response, step, state.CurrentRound, sessionID, roundStart)
	if verdict.isDone {
		// Guard against empty content: when the LLM stops naturally with no
		// content and no tool calls (e.g., thinking-only loop without KB),
		// retry with a nudge message instead of accepting an empty answer.
		if verdict.emptyContent {
			guards.emptyRetries++
			if guards.emptyRetries <= maxEmptyResponseRetries {
				state.PendingSteerMessages = step.UserMessagesBefore
				logger.Warnf(ctx, "[Agent][Round-%d] Empty content with stop - retrying (%d/%d)",
					round, guards.emptyRetries, maxEmptyResponseRetries)
				*messagesPtr = append(*messagesPtr, chat.Message{
					Role:    "user",
					Content: "Please provide your complete answer now as plain text.",
				})
				return iterOutcomeContinue, nil
			}
			// Retries exhausted — use fallback message rather than empty answer.
			// analyzeResponse emitted nothing for the empty rounds (they were
			// retryable), so the fallback must be emitted here as the turn's
			// sole terminal answer event (#2906).
			logger.Warnf(ctx, "[Agent][Round-%d] Empty content after %d retries - using fallback",
				round, maxEmptyResponseRetries)
			fallback := stalledAnswerFallback
			e.commitTerminalResult(ctx, state, sessionID, fallback, "", "failed", false)
			state.RoundSteps = append(state.RoundSteps, verdict.step)
			return iterOutcomeBreak, nil
		}
		// Loop-end inject: a user message queued while this finishing round
		// ran should keep the agent going instead of emitting a final answer.
		// Content-filter stops are terminal and do not take this path.
		// Past MaxIterations only the first inject gets an extra round;
		// anything after that stays in the queue for a follow-up turn.
		if response.FinishReason != "content_filter" {
			nextRound := state.CurrentRound + 1
			canContinue := e.withinIterationBudget(nextRound) || e.steerOverruns < maxSteerOverruns
			if canContinue {
				*messagesPtr = append(*messagesPtr, chat.Message{
					Role:               "assistant",
					Content:            verdict.finalAnswer,
					ReasoningContent:   response.ReasoningContent,
					ReasoningSignature: response.ReasoningSignature,
					ReasoningMetadata:  response.ReasoningMetadata,
				})
				injected := e.drainSteerMessages(ctx, state, messagesPtr, sessionID, assistantMessageID)
				if injected > 0 {
					e.clearTaskToolProjection(messagesPtr)
					e.taskPlan = nil // The user supplied new requirements.
				}
				if injected > 0 {
					verdict.step.IntermediateAnswer = true
					state.RoundSteps = append(state.RoundSteps, verdict.step)
					if !e.withinIterationBudget(nextRound) {
						e.steerOverruns++
						e.allowSteerOverrun = true
					}
					return iterOutcomeNext, nil
				}
			}
		}
		state.RoundSteps = append(state.RoundSteps, verdict.step)
		outcome := "answer"
		if runtimeAnswerFailure {
			outcome = "failed"
		} else if verdict.truncated {
			outcome = "partial"
		}
		e.commitTerminalResult(ctx, state, sessionID, verdict.finalAnswer, verdict.answerID, outcome, verdict.truncated)
		return iterOutcomeBreak, nil
	}

	// This round is non-terminal (it will execute tools and loop again). Any
	// plain assistant text streamed live to the answer area this round was a
	// preamble (e.g. "let me search the knowledge base…"), not the final
	// answer. No explicit retraction signal is emitted: the agent only ends by
	// stopping naturally with plain text and no tool calls, so the upcoming
	// tool-call events are themselves the authoritative "that wasn't the final
	// answer" marker. Both the stream handler and the UI treat any answer text
	// preceding a tool call in the same stream as a preamble and relocate it
	// into the steps tree. The preamble is still preserved as this round's
	// Thought.

	// 3. Act: Execute tool calls
	e.executeToolCalls(ctx, response, &step, state.CurrentRound, sessionID, assistantMessageID)
	toolCallCount = len(step.ToolCalls)

	// 4. Observe: Add tool results to messages and write to context
	state.RoundSteps = append(state.RoundSteps, step)
	*messagesPtr = e.appendToolResults(*messagesPtr, step)
	*messagesPtr = e.appendToolImages(ctx, *messagesPtr, step)
	common.PipelineInfo(ctx, "Agent", "round_end", map[string]interface{}{
		"iteration":   state.CurrentRound,
		"round":       round,
		"tool_calls":  toolCallCount,
		"thought_len": len(step.Thought),
	})

	return iterOutcomeNext, nil
}

func usageHasCounts(usage types.TokenUsage) bool {
	return usage.PromptTokens != 0 || usage.CompletionTokens != 0 || usage.TotalTokens != 0 ||
		usage.CacheReadTokens != 0 || usage.CacheWriteTokens != 0 || usage.CacheMissTokens != 0 || usage.CacheReported
}

// Auxiliary usage belongs to the call that returned it, even when an
// overflow recovery replaces that call's primary response with a retry.
func accumulateAuxiliaryUsage(state *types.AgentState, response *types.ChatResponse) {
	if state != nil && response != nil && usageHasCounts(response.AuxiliaryUsage) {
		state.TurnUsage.Accumulate(response.AuxiliaryUsage)
	}
}

// String returns a stable label for Langfuse output payloads.
func (o iterOutcome) String() string {
	switch o {
	case iterOutcomeNext:
		return "next"
	case iterOutcomeContinue:
		return "continue"
	case iterOutcomeBreak:
		return "break"
	default:
		return "unknown"
	}
}

// ---------------------------------------------------------------------------
// Tool result image VLM description helpers
// ---------------------------------------------------------------------------

const (
	toolImageBriefPrompt = types.ImageReadBriefPrompt
	toolImageFullPrompt  = types.ImageReadFullPrompt
	toolImageFocusRunes  = types.ImageReadFocusRunes
)

func toolImagePrompt(detail, focus string) string { return types.ImageReadPrompt(detail, focus) }
func toolImagePrompts(result *types.ToolResult) []string {
	prompt := toolImagePrompt(result.ImageDetail, result.ImageFocus)
	prompts := make([]string, len(result.Images))
	for i := range prompts {
		prompts[i] = prompt
	}
	return prompts
}

const (
	toolImageDescribeConcurrency = 4
	toolImageDescribeTimeout     = 120 * time.Second
)

// describeImages asks the configured VLM to describe tool result images, a
// few at a time, each under its own timeout so a slow image cannot starve the
// rest. prompts is aligned with the images (nil means a brief description for
// each). The result is aligned with the input; an image that could not be
// described is left empty.
func (e *AgentEngine) describeImages(ctx context.Context, imageDataURIs, prompts []string) []string {
	descriptions := make([]string, len(imageDataURIs))
	if e.imageDescriber == nil {
		return descriptions
	}
	sem := e.imageDescribeSlots
	if sem == nil {
		sem = make(chan struct{}, toolImageDescribeConcurrency)
	}
	var wg sync.WaitGroup
	for i, dataURI := range imageDataURIs {
		if ctx.Err() == nil {
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
			}
		}
		if ctx.Err() != nil {
			logger.Warnf(ctx, "[Agent] Context cancelled, skipping remaining %d tool result images", len(imageDataURIs)-i)
			break
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			imgBytes, err := decodeDataURIBytes(dataURI)
			if err != nil {
				logger.Warnf(ctx, "[Agent] Failed to decode tool result image %d: %v", i, err)
				return
			}
			imageCtx, cancel := context.WithTimeout(ctx, toolImageDescribeTimeout)
			defer cancel()
			prompt := toolImageBriefPrompt
			if i < len(prompts) {
				prompt = prompts[i]
			}
			desc, err := e.imageDescriber(imageCtx, imgBytes, prompt)
			if err != nil {
				logger.Warnf(ctx, "[Agent] VLM analysis failed for tool result image %d: %v", i, err)
				return
			}
			descriptions[i] = strings.TrimSpace(desc)
		}()
	}
	wg.Wait()
	return descriptions
}

// decodeDataURIBytes extracts raw bytes from a "data:mime;base64,..." URI.
// Retries with RawStdEncoding when standard base64 decoding fails (some MCP
// servers omit trailing '=' padding).
func decodeDataURIBytes(dataURI string) ([]byte, error) {
	if !strings.HasPrefix(dataURI, "data:") {
		return nil, fmt.Errorf("not a data URI")
	}
	idx := strings.Index(dataURI, ";base64,")
	if idx < 0 {
		return nil, fmt.Errorf("unsupported data URI encoding (expected base64)")
	}
	raw := dataURI[idx+8:]
	decoded, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		// Retry without padding — some MCP servers omit trailing '='
		decoded, err = base64.RawStdEncoding.DecodeString(raw)
	}
	return decoded, err
}

// beginCandidateRevision builds the existing isolated revision context for
// a rejected draft, or the legacy source-audit path when no workbench critic
// is configured. Exact successful execution remains separate from the draft.
func (e *AgentEngine) beginCandidateRevision(ctx context.Context, state *types.AgentState, messagesPtr *[]chat.Message, guards *loopGuards, query, candidate string) {
	// A second rejection refers to the latest candidate, not the first draft.
	// Restore real tool messages before rebuilding its isolated evidence view.
	restored, matched := restoreStructuredReviewConversation(*messagesPtr, guards)
	if guards.structuredReviewInput != "" && !matched {
		// Compaction may have replaced the exact review envelope. Keep its
		// saved real transcript intact and revise the latest draft in-place;
		// never wrap a synthetic review system as a new trusted base.
		*messagesPtr = append(restored, chat.Message{Role: "assistant", Content: candidate})
		if handoff, ok := reviewEvidenceHandoffMessage(e.turnReview.ReadEvidence); ok {
			*messagesPtr = append(*messagesPtr, handoff)
		}
		e.turnReview.ReadEvidence = nil
		guards.structuredAnswerReviewed = true
		e.structuredReviewMode = true
		return
	}
	*messagesPtr = restored
	guards.structuredAnswerReviewed = true
	e.structuredReviewMode = true
	guards.structuredReviewEvidence = append([]chat.Message(nil), (*messagesPtr)...)
	unsupported := []string{}
	if needsStructuredAnswerReviewForQuery(query, state.RoundSteps, *messagesPtr, candidate) {
		unsupported = ungroundedStructuredNumbers(candidate, answerNumericEvidence(candidate, state.RoundSteps, *messagesPtr), state.RoundSteps)
	}
	common.PipelineInfo(ctx, "Agent", "candidate_revision_start", map[string]interface{}{"iteration": state.CurrentRound, "unmatched_numbers": unsupported})
	catalogMessages := documentCatalogMessagesFromSteps(state.RoundSteps)
	sourceMessages := e.sourceConsistencyMessages(state, *messagesPtr)
	rowCompanion := structuredQueryRowReviewEvidence(state, sourceMessages, structuredQueryRowReviewBudget)
	documentCompanion := documentRetrievalCompanion(sourceMessages, documentRetrievalCompanionBudget)
	citationBudget := citationReviewEvidenceBudget
	toolBudget := 48000
	if e.needsSourceConsistencyCheck(state) {
		citationBudget = sourceReviewCitationBudget
		toolBudget = sourceReviewToolResultsBudget
		documentCompanion = documentRetrievalCompanionWithBudget(sourceMessages, sourceReviewDocumentBudget)
	}
	reviewConversation := e.semanticReviewConversation(*messagesPtr)
	reviewMessages := []chat.Message{}
	if len(*messagesPtr) > 0 && (*messagesPtr)[0].Role == "system" {
		reviewMessages = append(reviewMessages, (*messagesPtr)[0])
	}
	reviewMessages = append(reviewMessages, sourceMessages...)
	*messagesPtr = independentStructuredReviewWithSourceBudgets(
		reviewMessages, reviewConversation, catalogMessages, rowCompanion, documentCompanion, query, candidate, toolBudget, citationBudget, unsupported)
	*messagesPtr = e.bindIndependentReviewCitations(*messagesPtr, sourceMessages)
	if e.turnReview.ReadEvidence != nil {
		if !attachReviewEvidenceHandoff(*messagesPtr, e.turnReview.ReadEvidence) {
			if handoff, ok := reviewEvidenceHandoffMessage(e.turnReview.ReadEvidence); ok {
				*messagesPtr = append(*messagesPtr, handoff)
			}
		}
		e.turnReview.ReadEvidence = nil
	}
	e.attachTaskPlanToReview(*messagesPtr)
	guards.structuredReviewInput = (*messagesPtr)[1].Content
	e.contextRewrites++
	e.lastUsage = types.TokenUsage{}
	e.lastSentMsgCount = 0
}
