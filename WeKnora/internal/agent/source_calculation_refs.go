package agent

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/types"
)

const sourceReferenceCatalogBudget = 16 * 1024
const sourceReferencePoolLimit = 128
const sourceReferenceBatchLimit = 128

var compactSourceCalculationSchema = json.RawMessage(`{"type":"object","additionalProperties":false,"required":["operation"],"properties":{"operation":{"type":"string","enum":["inspect","calculate"]},"chunk_id":{"type":"string","description":"inspect: actual current-turn read chunk handle"},"source_fingerprint":{"type":"string","description":"inspect pagination: exact fingerprint returned by the prior page"},"offset":{"type":"integer","minimum":0},"limit":{"type":"integer","minimum":1,"maximum":128},"refs":{"type":"object","maxProperties":128,"propertyNames":{"pattern":"^[A-Za-z_][A-Za-z0-9_]{0,31}$"},"additionalProperties":{"type":"object","additionalProperties":false,"required":["ref","unit"],"properties":{"ref":{"type":"string","maxLength":80},"unit":{"type":"string","maxLength":128,"description":"Exact unit present in the same returned source context; empty means unknown and cannot yield %. Do not infer a unit."}}}},"calculations":{"type":"array","minItems":1,"maxItems":128,"items":{"type":"object","additionalProperties":false,"required":["expression","result_unit"],"properties":{"expression":{"type":"string","minLength":1,"maxLength":1024},"result_unit":{"type":"string","enum":["","%"]}}}}}}`)

const compactSourceCalculationInstructions = `Inspect actual current-turn source numbers, then calculate using only issued short refs and formulas. For EMF cached date/value text, captions and paragraphs use operation=inspect, chunk_id; follow next_offset with the exact source_fingerprint when refs are paginated. Catalog refs identify exact original numeric positions, not business meaning: select the needed values and units from the full user request and actual context. Dates, IDs, code and metadata are not observations. For real Markdown tables prefer calculate_document_table or analyze_document_tables for one row-range formula.
For operation=calculate send a shared refs directory {a:{ref:"returned sv:...",unit:"exact source unit"},...} and calculations:[{expression:"a/b-1",result_unit:"%"},...]. Never copy value, quote, chunk_id or inputs into a calculation; the server reads and binds them. Reuse refs across formulas; a formula uses only the variables it names, never pad it with zero times unused inputs. At most 128 refs and 128 formulas; each formula uses at most 32 variables. Only variable names, four arithmetic operators, parentheses and constants 0/1; result_unit=% already scales automatically. Unknown units stay unknown and cannot produce %. An issued ref must still match its actual current source version/scope. Missing, failed, stale or uninspected sources are unavailable, never silently substituted. No new source read, network or model call is executed here.`

type sourceNumberReference struct {
	ChunkID, Fingerprint, Version, Value string
	Start, End                           int // Original UTF-8 byte coordinates.
}

type sourceCalculationReferences struct {
	mu       sync.Mutex
	nonce    string
	registry *agenttools.ToolRegistry
	issued   map[string]sourceNumberReference
	keys     map[string]string
}

type compactSourceReferenceInput struct {
	Ref  string `json:"ref"`
	Unit string `json:"unit"`
}

type compactSourceFormula struct {
	Expression string `json:"expression"`
	ResultUnit string `json:"result_unit"`
}

type compactSourceRequest struct {
	Operation         string                                 `json:"operation"`
	ChunkID           string                                 `json:"chunk_id,omitempty"`
	SourceFingerprint string                                 `json:"source_fingerprint,omitempty"`
	Offset            int                                    `json:"offset,omitempty"`
	Limit             int                                    `json:"limit,omitempty"`
	Refs              map[string]compactSourceReferenceInput `json:"refs,omitempty"`
	Calculations      []compactSourceFormula                 `json:"calculations,omitempty"`
}

func (tool *sourceCalculationTool) configureCompactReferences(registries ...*agenttools.ToolRegistry) {
	state := &sourceCalculationReferences{issued: map[string]sourceNumberReference{}, keys: map[string]string{}}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err == nil {
		state.nonce = base64.RawURLEncoding.EncodeToString(nonce[:])
	}
	if len(registries) > 0 {
		state.registry = registries[0]
	}
	tool.compact = state
	// The execution registry also normalizes arguments. Accept both protocols
	// there; model projection advertises only the compact one for the workbench.
	var legacy, compact map[string]any
	if json.Unmarshal(tool.Parameters(), &legacy) != nil || json.Unmarshal(compactSourceCalculationSchema, &compact) != nil {
		return
	}
	properties := legacy["properties"].(map[string]any)
	compactProperties := compact["properties"].(map[string]any)
	legacyCalculations := properties["calculations"].(map[string]any)
	compactCalculations := compactProperties["calculations"].(map[string]any)
	legacyItem := legacyCalculations["items"].(map[string]any)
	itemProperties := legacyItem["properties"].(map[string]any)
	for key, value := range compactCalculations["items"].(map[string]any)["properties"].(map[string]any) {
		if _, exists := itemProperties[key]; !exists {
			itemProperties[key] = value
		}
	}
	// Keep one typed object projection so normalization can still losslessly
	// coerce legacy decimal tokens under inputs.value. Execute owns the two
	// disjoint protocols, including legacy's required inputs and batch limit.
	legacyItem["required"] = []string{"expression", "result_unit"}
	for key, value := range compactProperties {
		properties[key] = value
	}
	legacyCalculations["maxItems"], legacyCalculations["items"] = sourceReferenceBatchLimit, legacyItem
	properties["calculations"] = legacyCalculations
	delete(legacy, "required")
	legacy["anyOf"] = []any{map[string]any{"required": []string{"calculations"}}, map[string]any{"required": []string{"operation"}}}
	encoded, _ := json.Marshal(legacy)
	tool.BaseTool = agenttools.NewBaseTool(tool.Name(), tool.Description(), encoded)
}

func (tool *sourceCalculationTool) compactModelDefinition() types.FunctionDefinition {
	return types.FunctionDefinition{Name: tool.Name(), Description: compactSourceCalculationInstructions, Parameters: compactSourceCalculationSchema}
}

func compactSourceReadHint(call types.ToolCall) string {
	if call.Result == nil || !call.Result.Success || (call.ExecutionName() != "read_document" && call.ExecutionName() != "search_knowledge") {
		return ""
	}
	ids := []string{}
	seen := map[string]bool{}
	for _, chunk := range fetchedDocumentChunks(call) {
		if chunk.ID != "" && chunk.KnowledgeID != "" && !seen[chunk.ID] {
			seen[chunk.ID] = true
			if len(ids) < 8 {
				ids = append(ids, chunk.ID)
			}
		}
	}
	if len(ids) == 0 {
		return ""
	}
	encoded, _ := json.Marshal(map[string]any{"source_number_reference_access": map[string]any{
		"tool": sourceCalculationToolName, "operation": "inspect", "chunk_ids": ids, "omitted_chunk_count": len(seen) - len(ids),
		"notice": "For source arithmetic inspect actual numbers by chunk, then submit returned short refs and formulas; the server extracts values/quotes. This directory is navigation, never calculated evidence. For parsed Markdown tables use the row-range table calculator. No need to transcribe long quotes or declare every pool input in each formula.",
	}})
	return string(encoded)
}

func compactSourceFailure(err error) (*types.ToolResult, error) {
	data := map[string]any{"kind": "source_calculation", "status": "error", "error": err.Error(), "notice": "No calculation result was issued. Use current inspect refs and correct the specified source/unit/formula issue; unavailable evidence is not zero or proof of absence."}
	encoded, _ := json.Marshal(data)
	return &types.ToolResult{Success: false, Error: err.Error(), Data: data, Output: string(encoded)}, nil
}

func (tool *sourceCalculationTool) executeCompactReferences(ctx context.Context, raw json.RawMessage) (*types.ToolResult, error) {
	if len(raw) > 256*1024 || tool.steps == nil || tool.compact == nil || tool.compact.nonce == "" {
		return compactSourceFailure(fmt.Errorf("compact source request or actual source runtime is unavailable"))
	}
	var request compactSourceRequest
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return compactSourceFailure(fmt.Errorf("invalid compact calculation protocol: %w", err))
	}
	if decoder.Decode(new(any)) != io.EOF || ctx.Err() != nil {
		return compactSourceFailure(fmt.Errorf("compact request is incomplete or canceled"))
	}
	switch request.Operation {
	case "inspect":
		if len(request.Refs) > 0 || len(request.Calculations) > 0 || request.ChunkID == "" || len(request.ChunkID) > 256 || request.Offset < 0 || request.Limit < 0 || request.Limit > 128 {
			return compactSourceFailure(fmt.Errorf("inspect requires one real chunk and bounded offset/limit, without calculations"))
		}
		return tool.inspectSourceReferences(ctx, request)
	case "calculate":
		if request.ChunkID != "" || request.SourceFingerprint != "" || request.Offset != 0 || request.Limit != 0 || len(request.Refs) < 1 || len(request.Refs) > sourceReferencePoolLimit || len(request.Calculations) < 1 || len(request.Calculations) > sourceReferenceBatchLimit {
			return compactSourceFailure(fmt.Errorf("calculate requires 1 to 128 shared refs and 1 to 128 formulas; source coordinates come from inspect only"))
		}
		return tool.calculateSourceReferences(ctx, request)
	default:
		return compactSourceFailure(fmt.Errorf("operation must be inspect or calculate"))
	}
}

func (tool *sourceCalculationTool) currentReferenceSource(chunkID, fingerprint string) (documentTableSource, error) {
	var document string
	var latest, selected *documentTableSource
	for _, source := range documentTableSources(tool.steps(), tool.compact.registry) {
		if source.Chunk.ID != chunkID {
			continue
		}
		if document != "" && document != source.Chunk.KnowledgeID {
			return documentTableSource{}, fmt.Errorf("chunk identity has conflicting source documents; reread the exact authorized source")
		}
		document = source.Chunk.KnowledgeID
		// Table provenance already preserves the original source revision across
		// safe prose projections. Scalar reads also expose attached captions/OCR;
		// a changed attachment must not keep an old observation's numeric refs.
		source.Version, _ = documentTableDigest([]any{source.Version, source.Chunk.Images})
		copy := source
		latest = &copy
		if fingerprint == "" || source.Fingerprint == fingerprint {
			selected = &copy
		}
	}
	if selected == nil {
		return documentTableSource{}, fmt.Errorf("chunk or fingerprint is not an actual successful current scoped source read; read and inspect it first")
	}
	if latest.Version != selected.Version {
		return documentTableSource{}, fmt.Errorf("selected source has a newer actual version; inspect the current read again")
	}
	return *selected, nil
}

// Keep the existing source-prose filters, while masking instead of deleting
// bytes so every issued span still addresses the exact original UTF-8 source.
func sourceReferenceNumericMask(content string) string {
	masked := []byte(content)
	mask := func(start, end int) {
		for i := start; i < end; i++ {
			masked[i] = ' '
		}
	}
	maskPattern := func(pattern *regexp.Regexp) {
		for _, span := range pattern.FindAllStringIndex(string(masked), -1) {
			mask(span[0], span[1])
		}
	}
	maskPattern(documentCodeFence)
	maskPattern(documentFakeToolEnvelope)
	for i := 0; i < len(masked); i++ {
		if masked[i] != '`' {
			continue
		}
		end := i + 1
		for end < len(masked) && masked[end] == '`' {
			end++
		}
		run := end - i
		for close := end; close < len(masked); {
			if masked[close] != '`' {
				close++
				continue
			}
			after := close + 1
			for after < len(masked) && masked[after] == '`' {
				after++
			}
			if after-close == run {
				mask(i, after)
				i = after - 1
				break
			}
			close = after
		}
	}
	for i := 0; i < len(masked); i++ {
		if masked[i] == '{' || masked[i] == '[' {
			decoder := json.NewDecoder(strings.NewReader(string(masked[i:])))
			var embedded any
			if decoder.Decode(&embedded) == nil {
				end := i + int(decoder.InputOffset())
				mask(i, end)
				i = end - 1
			}
		}
	}
	maskPattern(sourceCitationTag)
	maskPattern(sourceOpaqueToken)
	for _, match := range documentMarkdownLink.FindAllStringSubmatchIndex(string(masked), -1) {
		label := append([]byte(nil), masked[match[2]:match[3]]...)
		mask(match[0], match[1])
		copy(masked[match[2]:match[3]], label)
	}
	maskPattern(documentSourceURL)
	for _, span := range calendarDateLiteral.FindAllStringIndex(string(masked), -1) {
		if standaloneDateToken(content, span[0], span[1]) && validCalendarLiteral(content[span[0]:span[1]]) {
			mask(span[0], span[1])
		}
	}
	maskPattern(cellOrListMarker)
	return string(masked)
}

func sourceReferenceNumbers(source documentTableSource) []sourceNumberReference {
	content := source.Chunk.Content
	masked := sourceReferenceNumericMask(content)
	refs := []sourceNumberReference{}
	continuation := func(r rune) bool {
		return r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r == '_' || r == '.' || r == ','
	}
	for _, span := range proseNumber.FindAllStringIndex(masked, -1) {
		if span[0] > 0 {
			before, _ := utf8.DecodeLastRuneInString(content[:span[0]])
			if continuation(before) {
				continue
			}
		}
		if span[1] < len(content) {
			after, _ := utf8.DecodeRuneInString(content[span[1]:])
			if continuation(after) {
				continue
			}
		}
		text := content[span[0]:span[1]]
		if !documentTableNumber.MatchString(text) {
			continue
		}
		value := strings.ReplaceAll(strings.ReplaceAll(text, ",", ""), "−", "-")
		if len(value) > 128 || !sourceCalculationDecimal.MatchString(value) {
			continue
		}
		refs = append(refs, sourceNumberReference{ChunkID: source.Chunk.ID, Fingerprint: source.Fingerprint, Version: source.Version, Value: value, Start: span[0], End: span[1]})
	}
	return refs
}

func sourceReferenceContext(content string, start, end, radius int) (string, int) {
	before, after := []rune(content[:start]), []rune(content[end:])
	from := max(0, len(before)-radius)
	return string(before[from:]) + content[start:end] + string(after[:min(radius, len(after))]), from
}

func (tool *sourceCalculationTool) inspectSourceReferences(ctx context.Context, request compactSourceRequest) (*types.ToolResult, error) {
	source, err := tool.currentReferenceSource(request.ChunkID, request.SourceFingerprint)
	if err != nil {
		return compactSourceFailure(err)
	}
	all := sourceReferenceNumbers(source)
	if request.Offset > len(all) {
		return compactSourceFailure(fmt.Errorf("inspect offset exceeds the actual numeric-reference count"))
	}
	limit := request.Limit
	if limit == 0 {
		limit = 64
	}
	data := map[string]any{"kind": "source_calculation_catalog", "status": "ok", "chunk_id": source.Chunk.ID, "knowledge_id": source.Chunk.KnowledgeID,
		"source_fingerprint": source.Fingerprint, "source_version": source.Version, "offset": request.Offset, "total_refs": len(all),
		"refs": []any{}, "complete": false, "notice": "Exact current-read numeric positions for model selection, not computations or metric/unit interpretation. Dates, identifiers and embedded code/JSON are excluded. Follow next_offset with this source_fingerprint; unseen refs are not issued. Units must come from the same real source context; empty remains unknown."}
	tool.compact.mu.Lock()
	defer tool.compact.mu.Unlock()
	entries := []any{}
	pending := map[string]sourceNumberReference{}
	pendingKeys := map[string]string{}
	index := request.Offset
	for ; index < len(all) && len(entries) < limit; index++ {
		if ctx.Err() != nil {
			return compactSourceFailure(ctx.Err())
		}
		item := all[index]
		key := item.Fingerprint + ":" + strconv.Itoa(item.Start) + ":" + strconv.Itoa(item.End)
		id := tool.compact.keys[key]
		if id == "" {
			id = "sv:" + tool.compact.nonce + ":" + strconv.Itoa(len(tool.compact.issued)+len(pending)+1)
		}
		contextText, contextStart := sourceReferenceContext(source.Chunk.Content, item.Start, item.End, 72)
		entry := map[string]any{"ref": id, "value": item.Value, "source_value": source.Chunk.Content[item.Start:item.End], "start_rune": utf8.RuneCountInString(source.Chunk.Content[:item.Start]), "end_rune": utf8.RuneCountInString(source.Chunk.Content[:item.End]), "context": contextText, "context_start_rune": contextStart}
		data["refs"] = append(append([]any(nil), entries...), entry)
		encoded, _ := json.Marshal(data)
		if len(encoded) > sourceReferenceCatalogBudget-160 {
			break
		}
		if len(tool.compact.issued)+len(pending) >= 8192 && tool.compact.keys[key] == "" {
			return compactSourceFailure(fmt.Errorf("source reference inventory budget exhausted; use a smaller current source scope"))
		}
		entries = append(entries, entry)
		if tool.compact.keys[key] == "" {
			pending[id], pendingKeys[key] = item, id
		}
	}
	data["refs"], data["complete"] = entries, index == len(all)
	if index < len(all) {
		data["next_offset"] = index
	}
	encoded, _ := json.Marshal(data)
	if len(encoded) > sourceReferenceCatalogBudget {
		return compactSourceFailure(fmt.Errorf("source reference catalog exceeds the bounded response budget"))
	}
	if err := ctx.Err(); err != nil {
		return compactSourceFailure(err)
	}
	// Publish the page atomically. No rejected or canceled response can issue
	// guessable later ordinals that were never actually returned to the model.
	for id, item := range pending {
		tool.compact.issued[id] = item
	}
	for key, id := range pendingKeys {
		tool.compact.keys[key] = id
	}
	return &types.ToolResult{Success: true, Data: data, Output: string(encoded)}, nil
}

func sourceFormulaVariables(expression string) []string {
	seen := map[string]bool{}
	for i := 0; i < len(expression); {
		if expression[i] >= 'A' && expression[i] <= 'Z' || expression[i] >= 'a' && expression[i] <= 'z' || expression[i] == '_' {
			start := i
			for i < len(expression) && (expression[i] >= 'A' && expression[i] <= 'Z' || expression[i] >= 'a' && expression[i] <= 'z' || expression[i] >= '0' && expression[i] <= '9' || expression[i] == '_') {
				i++
			}
			seen[expression[start:i]] = true
		} else {
			i++
		}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// A bounded original substring can cut across an excluded code/JSON boundary.
// Prefer the largest bounded window that still passes the existing complete
// source-prose comparison. Do not reconstruct text, borrow a unit, or relax
// validation when the nearby source cannot support the selected unit.
func compactSourceReferenceQuote(source documentTableSource, binding sourceNumberReference, unit string) string {
	content := source.Chunk.Content
	if len(content) <= 8192 {
		return content
	}
	cleanContent := documentNumericProse(content)
	value, _ := new(big.Rat).SetString(binding.Value)
	var safe string
	for _, radius := range []int{900, 450, 225, 112, 56, 28, 14, 7, 3, 1, 0} {
		quote, _ := sourceReferenceContext(content, binding.Start, binding.End, radius)
		cleanQuote := documentNumericProse(quote)
		if strings.TrimSpace(cleanQuote) == "" || !strings.Contains(cleanContent, cleanQuote) || value == nil || !sourceQuoteContainsNumber(cleanQuote, value) {
			continue
		}
		if safe == "" {
			safe = quote
		}
		if unit == "" || strings.Contains(cleanQuote, unit) {
			return quote
		}
	}
	if safe != "" {
		return safe
	}
	return content[binding.Start:binding.End]
}

func (tool *sourceCalculationTool) calculateSourceReferences(ctx context.Context, request compactSourceRequest) (*types.ToolResult, error) {
	bindings := map[string]sourceNumberReference{}
	sources := map[string]documentTableSource{}
	for name, input := range request.Refs {
		if !sourceCalculationVariable.MatchString(name) || len(input.Unit) > 128 {
			return compactSourceFailure(fmt.Errorf("invalid reference variable or source unit"))
		}
		tool.compact.mu.Lock()
		binding, found := tool.compact.issued[input.Ref]
		tool.compact.mu.Unlock()
		if !found {
			return compactSourceFailure(fmt.Errorf("variable %s uses a ref not issued by this current inspect runtime", name))
		}
		source, ok := sources[binding.Fingerprint]
		if !ok {
			var err error
			source, err = tool.currentReferenceSource(binding.ChunkID, binding.Fingerprint)
			if err != nil || source.Version != binding.Version {
				return compactSourceFailure(fmt.Errorf("variable %s source is stale or outside current scope; inspect the current actual read again", name))
			}
			sources[binding.Fingerprint] = source
		}
		bindings[name] = binding
	}
	quotes := map[string]sourceCalculationQuote{}
	quoteKeys := map[string]string{}
	results := []any{}
	used := map[string]bool{}
	for index, formula := range request.Calculations {
		if err := ctx.Err(); err != nil {
			return compactSourceFailure(err)
		}
		names := sourceFormulaVariables(formula.Expression)
		if len(names) < 1 || len(names) > 32 {
			return compactSourceFailure(fmt.Errorf("formula %d requires 1 to 32 actually used source variables", index+1))
		}
		inputs := map[string]sourceCalculationInput{}
		chunks := map[string][]fetchedDocumentChunk{}
		for _, name := range names {
			binding, ok := bindings[name]
			if !ok {
				return compactSourceFailure(fmt.Errorf("formula %d uses undeclared source ref %s", index+1, name))
			}
			source := sources[binding.Fingerprint]
			quote := compactSourceReferenceQuote(source, binding, request.Refs[name].Unit)
			key := binding.Fingerprint + "\x00" + quote
			quoteID := quoteKeys[key]
			if quoteID == "" {
				quoteID = "q" + strconv.Itoa(len(quotes)+1)
				quotes[quoteID], quoteKeys[key] = sourceCalculationQuote{ChunkID: binding.ChunkID, Quote: quote}, quoteID
			}
			inputs[name] = sourceCalculationInput{Value: binding.Value, ChunkID: binding.ChunkID, Quote: quote, QuoteRef: quoteID, Unit: request.Refs[name].Unit}
			chunks[binding.ChunkID] = append(chunks[binding.ChunkID], source.Chunk)
			used[name] = true
		}
		result, err := executeSourceCalculation(sourceCalculationPlan{Expression: formula.Expression, Inputs: inputs, ResultUnit: formula.ResultUnit}, chunks)
		if err != nil {
			return compactSourceFailure(fmt.Errorf("formula %d: %w", index+1, err))
		}
		proofs := result["inputs"].(map[string]any)
		for _, name := range names {
			proof := proofs[name].(map[string]any)
			binding := bindings[name]
			proof["source_ref"], proof["source_fingerprint"], proof["source_version"] = request.Refs[name].Ref, binding.Fingerprint, binding.Version
			proof["start_rune"], proof["end_rune"] = utf8.RuneCountInString(sources[binding.Fingerprint].Chunk.Content[:binding.Start]), utf8.RuneCountInString(sources[binding.Fingerprint].Chunk.Content[:binding.End])
		}
		results = append(results, result)
	}
	data := map[string]any{"kind": "source_calculation", "status": "ok", "calculations": results, "source_quotes": quotes,
		"unused_ref_count": len(bindings) - len(used), "notice": "Exact arithmetic over model-selected current source refs. Values and shared exact quotes were extracted by the server; only each formula's used inputs are evidence. Unused pool refs are not calculation inputs or additional authority. Source unit and formula checks are unchanged; no business scope or unit was inferred."}
	encoded, _ := json.Marshal(data)
	if len(encoded) > 256*1024 {
		return compactSourceFailure(fmt.Errorf("calculated source proof batch exceeds storage budget; split formulas without dropping evidence"))
	}
	return &types.ToolResult{Success: true, Data: data, Output: string(encoded)}, nil
}
