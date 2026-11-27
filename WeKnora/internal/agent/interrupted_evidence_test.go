package agent

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/Tencent/WeKnora/internal/agent/collaboration"
	"github.com/Tencent/WeKnora/internal/structuredresult"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestAf1KB28InterruptedPairUsesActualReadTableAndWorkbook(t *testing.T) {
	t.Setenv("WEKNORA_AGENT_DOCUMENT_TABLE_EVIDENCE", "true")
	for _, enabled := range []bool{false, true} {
		name := "shadow"
		if enabled {
			name = "enforced"
		}
		t.Run(name, func(t *testing.T) {
			// Archived Af1 native workbook response and actual stored report page
			// and OCR. Run the original read tool through its own source boundary.
			r := newQA4Stage2CReplay(t, "af1-kb28", enabled, false)
			r.trace.Calls = r.trace.Calls[:3] // A-1's successful bind, page read and workbook query.
			r.setPlan(StructuredTaskGoal{SourceMode: "open", VersionTask: "compare_sources", Table: "中国棉花平衡表",
				Metric: "工商业期末库存", SourceTime: "observed_period", Required: []string{"scoped_value"},
				Period: map[string]any{"from": "2023-10-01", "to": "2023-10-31", "grain": "month"}})
			db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "collaboration.db")))
			require.NoError(t, err)
			sql, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { _ = sql.Close() })
			require.NoError(t, db.AutoMigrate(&collaboration.Record{}))
			store := collaboration.NewStore(db)
			runtime := collaboration.New(store, nil)
			scope := collaboration.Scope{TenantID: 1, UserID: "qa4-replay-reader", SessionID: "s2c", ID: "turn"}
			binding, err := runtime.AcquireParent(r.ctx, scope)
			require.NoError(t, err)
			var earlier []json.RawMessage
			for page := range 100 {
				earlier = appendInterruptedObservation(earlier, interruptedDocumentObservation(coverageEvidence{
					SourceID: r.trace.Documents[1].ID, KnowledgeBaseID: qa4Stage2Library,
					ChunkID: r.trace.Chunks[0].ID, Page: page + 1,
				}))
			}
			require.NoError(t, binding.RecordParentEvidence(r.ctx, scope.ID, earlier))
			require.NoError(t, binding.RecordParentEvidence(r.ctx, scope.ID, earlier), "duplicate navigations share their physical page receipt")
			r.engine.SetCollaboration(binding)
			r.through(32)
			require.True(t, r.results[17].Result.Success)
			require.True(t, r.results[32].Result.Success)
			call := r.results[32]
			contract, validationErr := structuredresult.ValidatePayload(call.Result.Data)
			require.NoError(t, validationErr)
			require.NotNil(t, contract)
			observations := interruptedStructuredObservations(call, "query_series", call.ExecutionArgs())
			descriptor, _ := json.Marshal(call.Result.Data["_structured_execution"])
			require.NotEmpty(t, observations, "the native workbook must project, descriptor=%s", descriptor)
			r.engine.refreshEvidenceCompletion(r.ctx, r.trace.Question, r.state, r.messages)
			r.engine.checkpointInterruptedComparison(r.ctx, r.state, "unverified extra figures 876 and 516.2")
			persisted, err := collaboration.NewStore(db).Read(r.ctx, scope)
			require.NoError(t, err)
			var reportAuthorized bool
			answer := RenderInterruptedEvidence(r.ctx, persisted.ParentEvidence, func(source InterruptedSource) bool {
				if source.Structured {
					return source.KnowledgeID == "replay-book" && source.FileHash == r.trace.Documents[0].FileHash
				}
				reportAuthorized = reportAuthorized || source.ChunkID == r.trace.Chunks[1].ID &&
					source.ParentChunkID == r.trace.Chunks[0].ID && source.ContentSHA256 == candidateTextDigest(r.trace.Chunks[1].Content)
				return reportAuthorized
			}, false, InterruptedProgress{Known: true})
			require.True(t, reportAuthorized, "a checked table value must carry its exact OCR snapshot")
			require.Contains(t, answer, "331.2")
			require.Contains(t, answer, "G87")
			if types.VerifiedPublicationEnabled() {
				require.Contains(t, answer, "298.93")
				require.Contains(t, answer, "32.27")
				require.NotContains(t, answer, "298.93291576470585")
				require.Contains(t, answer, "给出的数字不同")
				require.Contains(t, answer, "本轮执行中断")
			} else {
				require.Contains(t, answer, "298.93291576470585")
				require.Contains(t, answer, "原报数值不同")
			}
			require.NotContains(t, answer, "876")
			require.NotContains(t, answer, "516.2")
			filtered := RenderInterruptedEvidence(r.ctx, persisted.ParentEvidence, func(source InterruptedSource) bool {
				return source.Structured
			}, false, InterruptedProgress{Known: true})
			require.Contains(t, filtered, "G87")
			require.NotContains(t, filtered, "331.2")
			require.NotContains(t, filtered, "原报数值不同")
			require.NotContains(t, filtered, "给出的数字不同")
			require.NotContains(t, filtered, types.MessageRunStateAwaitingInput)
		})
	}
}
