# Memory relevance per conversation turn

Long-term memory is admitted by the current chat model using the current original
user request and the complete authorized conversation preceding that turn. Stored
profile, preference, interest, explicit notes and facts are candidates; none is
automatically applicable. Current instructions override remembered defaults.
An unrelated task can legitimately use no memories.

The Agent and quick-answer paths load the original user and completed assistant
text from the authorized session, including earlier user corrections before a
cancelled reply. A stored summary supplements those messages. Tool payloads and
image bytes are excluded. A fixed persisted current-turn boundary prevents later
concurrent requests from entering the decision. Shared-agent retrieval/model
scope does not grant access to another workspace's conversations.
Personal memory and its embedding configuration remain in the authenticated
caller's workspace, while the chat model retains the authorized execution scope.
Accepted, successfully persisted mid-run user corrections are included in later
memory-tool decisions in their original order.

Lexical and vector retrieval produce candidates only. Lexical evidence from each
conversation message helps retrieve facts referred to by a short follow-up. One
structured model selection gate admits candidates, checks their IDs and rechecks
ownership, active status, validity and unchanged content after inference. The
`search_memory` tool uses this same gate and the server's original turn context;
its search query cannot redefine the user's task. Query rewriting reuses only
the current turn's selected memories, never the unfiltered background block.

The selection call has a 12-second deadline. Candidate and complete-conversation
budgets are checked before inference, with an additional check against the actual
model's context window. Missing model/context, incomplete pagination, oversized
history, transport failure, interrupted output or invalid selection yields no
memory injection. There is no fallback to unfiltered candidates or cached text,
and no truncation pretending to be complete history. This adds one bounded chat
model call when candidates exist; a memory search adds its own selection call.

The server persists and streams the exact admitted set. Empty replacement events
clear the current reply's memory display; additional memory-tool reads merge into
that reply. Earlier replies retain their own recorded sets.

This change does not migrate, delete or rewrite existing memory content. Normal
usage counters can still change when a memory is actually used. No question,
commodity, image-task keyword or model-name rule decides semantic relevance.
Background extraction retains its model authorization checks; a borrowed agent's
model ID does not by itself grant a personal workspace access to that model.

Validation covers unrelated tasks, topic changes, conditional preferences and
current overrides, paraphrases, follow-up references, early conversation facts,
cancelled-turn corrections, malformed model output, scope/state changes, empty
events and memory-tool reporting. The opt-in `TestMemoryRelevanceLiveModel` uses a
configured development chat model and an isolated synthetic in-memory database;
it does not load user memories or invoke image generation. Normal test runs skip
that paid acceptance unless private model input paths are explicitly supplied.
