package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
)

const (
	ToolStructuredCapabilities = "structured_capabilities"
	StructuredManifestTimeout  = 2 * time.Second
	structuredServiceName      = "structured_data"
)

// StructuredOperationDescriptor is runtime protocol metadata. It is constructed
// only from the fixed adapter's allowlist and the authenticated service schema;
// model arguments and tool-name prefixes cannot create one.
type StructuredOperationDescriptor struct {
	Operation       string `json:"canonical_operation"`
	ToolName        string `json:"tool_name"`
	SchemaSHA256    string `json:"schema_sha256,omitempty"`
	ManifestSHA256  string `json:"manifest_sha256,omitempty"`
	ProtocolVersion string `json:"protocol_version,omitempty"`
	Generation      uint64 `json:"generation,omitempty"`
	ScopeMode       string `json:"scope_mode"`
	EffectClass     string `json:"effect_class"`
}

type structuredOperationContract struct{ scope, effect string }

// This is a service protocol allowlist, never a business-intent classifier.
var structuredOperationContracts = map[string]structuredOperationContract{
	"describe_tools":      {"protocol", "protocol"},
	"list_datasets":       {"directory", "read"},
	"list_series":         {"knowledge_base", "read"},
	"list_charts":         {"knowledge_base", "read"},
	"query_series":        {"knowledge_base", "read"},
	"compare_series":      {"knowledge_base", "read"},
	"convert_price":       {"knowledge_base", "read"},
	"inspect_source":      {"knowledge_base", "read"},
	"get_result_evidence": {"knowledge_base", "session_state"},
	"analyze_table":       {"knowledge_base", "read"},
	"list_pdf_tables":     {"knowledge_base", "read"},
	"read_pdf_table":      {"knowledge_base", "read"},
	"query_pdf_table":     {"knowledge_base", "read"},
	"resolve_fields":      {"knowledge_base", "session_state"},
	"confirm_fields":      {"knowledge_base", "user_confirmation"},
	"execute_field_plan":  {"knowledge_base", "session_state"},
}

func StructuredOperationNames() []string {
	names := make([]string, 0, len(structuredOperationContracts)-1)
	for name := range structuredOperationContracts {
		if name != "describe_tools" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func structuredDescriptor(operation, name string) (StructuredOperationDescriptor, bool) {
	contract, ok := structuredOperationContracts[operation]
	return StructuredOperationDescriptor{Operation: operation, ToolName: name, ScopeMode: contract.scope, EffectClass: contract.effect}, ok
}

func structuredDigest(value any) string {
	raw, _ := json.Marshal(value)
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:])
}

type structuredInvocationKey struct{}

func withStructuredInvocation(ctx context.Context, descriptor StructuredOperationDescriptor) context.Context {
	return context.WithValue(ctx, structuredInvocationKey{}, descriptor)
}

// StructuredInvocation resolves only actual native registered adapters. A tool
// named structured_* supplied by another integration is not a native operation.
func (r *ToolRegistry) StructuredInvocation(name string, raw json.RawMessage) (StructuredOperationDescriptor, map[string]any, bool) {
	tool, err := r.GetTool(name)
	if err != nil {
		return StructuredOperationDescriptor{}, nil, false
	}
	var args map[string]any
	if json.Unmarshal(raw, &args) != nil || args == nil {
		return StructuredOperationDescriptor{}, nil, false
	}
	switch actual := tool.(type) {
	case *structuredReadOperationTool:
		return actual.descriptor, args, true
	case *StructuredDataTool:
		operation, _ := args["operation"].(string)
		descriptor, ok := structuredDescriptor(operation, name)
		actual.protocolMu.RLock()
		if current, exists := actual.protocolDescriptors[operation]; exists {
			descriptor = current
			descriptor.ToolName = name
		}
		actual.protocolMu.RUnlock()
		inner, _ := args["arguments"].(map[string]any)
		return descriptor, inner, ok && inner != nil
	case *structuredCapabilitiesTool:
		return StructuredOperationDescriptor{Operation: "describe_tools", ToolName: name, ScopeMode: "protocol", EffectClass: "protocol"}, args, true
	}
	return StructuredOperationDescriptor{}, nil, false
}

func (r *ToolRegistry) CanRunStructuredCallConcurrently(name, arguments string) bool {
	if descriptor, _, ok := r.StructuredInvocation(name, json.RawMessage(arguments)); ok {
		return descriptor.EffectClass == "read" || descriptor.Operation == "describe_tools" && name != ToolStructuredCapabilities
	}
	for op := range structuredOperationContracts {
		if name == "structured_"+op {
			return false
		}
	}
	return CanRunToolCallConcurrently(name, arguments)
}

// StructuredOperationFromCall reads a runtime-generated target, with strict
// legacy envelope fallback. It does not turn a matching prefix into authority.
func StructuredOperationFromCall(call types.ToolCall) (string, bool) {
	if call.Target != nil && call.Target.ServiceName == structuredServiceName && (call.Target.Name == ToolStructuredData || call.Target.Name == "structured_"+call.Target.ToolName) {
		_, ok := structuredOperationContracts[call.Target.ToolName]
		return call.Target.ToolName, ok
	}
	if call.ExecutionName() == ToolStructuredData {
		op, _ := call.ExecutionArgs()["operation"].(string)
		_, ok := structuredOperationContracts[op]
		return op, ok
	}
	// Old persisted typed calls predate canonical targets. Only exact built-in
	// protocol names are recognized for diagnostics, never fuzzy prefixes.
	for op := range structuredOperationContracts {
		if call.ExecutionName() == "structured_"+op {
			return op, true
		}
	}
	return "", false
}

func (r *ToolRegistry) structuredBase() *StructuredDataTool {
	tool, _ := r.GetTool(ToolStructuredData)
	base, _ := tool.(*StructuredDataTool)
	return base
}

// PrepareStructuredProtocol advertises the pure capability recovery entrypoint
// even when negotiation fails. Only the trusted adapter's own generic entry is
// deferred; unrelated first-wins registrations are never replaced or hidden.
func (r *ToolRegistry) PrepareStructuredProtocol() bool {
	base := r.structuredBase()
	if base == nil {
		return false
	}
	if r.deferred == nil {
		r.deferred = map[string]bool{}
	}
	r.deferred[ToolStructuredData] = true
	if r.structuredCapabilities == nil {
		r.structuredCapabilities = &structuredCapabilitySet{base: base, owned: map[string]*structuredReadOperationTool{}}
	}
	if _, err := r.GetTool(ToolStructuredCapabilities); err != nil {
		r.RegisterTool(&structuredCapabilitiesTool{BaseTool: NewBaseTool(ToolStructuredCapabilities,
			"查看当前真实结构化工具能力；首次调用自动恢复可信协议缓存或在短预算内协商，refresh=true 可重新协商。此入口只读协议，不查询来源、不选业务知识库、不执行数值查询。数据操作只使用当前展示的 structured_ 扁平参数定义；旧 structured_data 嵌套格式仅供历史兼容。缺少能力不表示来源无数据。",
			json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"refresh":{"type":"boolean","default":false}}}`)), registry: r})
	}
	return true
}

type structuredCapabilitySet struct {
	base       *StructuredDataTool
	owned      map[string]*structuredReadOperationTool
	manifest   string
	protocol   string
	generation uint64
	lastError  string
	refreshes  int
}

// InstallStructuredManifest runs at an execution barrier, after preceding reads
// have drained. Existing calls retain their captured adapter and descriptor.
// First-wins ownership is preserved for every unrelated registry entry.
func (r *ToolRegistry) InstallStructuredManifest(data map[string]any) (*types.ToolResult, error) {
	if !r.PrepareStructuredProtocol() {
		return nil, fmt.Errorf("没有已授权的结构化协议适配器")
	}
	set := r.structuredCapabilities
	tools := NewStructuredReadOperationTools(set.base, data)
	if len(tools) == 0 {
		return structuredProtocolFailure("manifest_unavailable", "未取得合法结构化工具定义；使用 structured_capabilities refresh=true 重新协商，不据此判断来源不存在", "manifest", false), nil
	}
	manifest, _ := data["manifest_sha256"].(string)
	protocol := ""
	if value, exists := data["protocol_version"]; exists {
		protocol = fmt.Sprint(value)
	}
	if manifest != "" {
		decoded, err := hex.DecodeString(manifest)
		if err != nil || len(decoded) != sha256.Size || strings.ToLower(manifest) != manifest || protocol == "" {
			return structuredProtocolFailure("manifest_invalid", "能力版本或哈希无效；未替换当前工具定义", "manifest", false), nil
		}
	}
	// An unversioned legacy service is negotiated afresh each turn. Its local
	// digest identifies the observed schema; it is not a server execution pin.
	localHash := structuredDigest(data)
	set.base.protocolMu.Lock()
	defer set.base.protocolMu.Unlock()
	set.generation++
	for name, owned := range set.owned {
		if r.tools[name] == owned {
			delete(r.tools, name)
			delete(r.deferred, name)
		}
	}
	set.owned = map[string]*structuredReadOperationTool{}
	set.base.protocolDescriptors = map[string]StructuredOperationDescriptor{}
	for _, value := range tools {
		tool := value.(*structuredReadOperationTool)
		tool.descriptor.ManifestSHA256, tool.descriptor.ProtocolVersion, tool.descriptor.Generation = manifest, protocol, set.generation
		tool.generation = set.generation
		set.base.protocolDescriptors[tool.operation] = tool.descriptor
		r.RegisterTool(tool)
		if r.tools[tool.Name()] == tool {
			set.owned[tool.Name()] = tool
		}
	}
	set.base.protocolGeneration = set.generation
	set.manifest, set.protocol, set.lastError = localHash, protocol, ""
	if manifest != "" {
		rememberStructuredManifest(set.base, data)
	}
	return r.structuredCapabilityResult(), nil
}

func (r *ToolRegistry) structuredCapabilityResult() *types.ToolResult {
	set := r.structuredCapabilities
	rows := []StructuredOperationDescriptor{}
	for _, name := range r.ListTools() {
		if tool, ok := set.owned[name]; ok && r.tools[name] == tool {
			rows = append(rows, tool.descriptor)
		}
	}
	data := map[string]any{"kind": "structured_capabilities", "status": "ok", "tools": rows,
		"generation": set.generation, "observed_manifest_sha256": set.manifest,
		"protocol_version": set.protocol, "source_data_read": false,
		"notice": "仅为协议能力。当前 structured_ 工具参数全部扁平；历史 nested structured_data 调用不代表当前格式。工具可用不授予来源权限，旧结果仍须按本轮权限和版本恢复。"}
	if len(rows) == 0 {
		data["status"] = "manifest_unavailable"
		data["code"] = "manifest_unavailable"
		data["retryable"] = true
		data["execution_started"] = false
		data["next_step"] = "使用 structured_capabilities refresh=true 在本轮预算内重试协议协商；当前未取得工具定义，不能据此判断来源无数据。"
	}
	if set.lastError != "" {
		data["last_error_code"] = set.lastError
	}
	encoded, _ := json.Marshal(data)
	return &types.ToolResult{Success: len(rows) > 0, Data: data, Output: string(encoded)}
}

type structuredCapabilitiesTool struct {
	BaseTool
	registry *ToolRegistry
}

func (t *structuredCapabilitiesTool) Execute(ctx context.Context, raw json.RawMessage) (*types.ToolResult, error) {
	var request struct {
		Refresh bool `json:"refresh"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if len(raw) > 1024 || decoder.Decode(&request) != nil || decoder.Decode(new(any)) != io.EOF {
		return nil, fmt.Errorf("能力参数需要 refresh 布尔值")
	}
	caller := types.CallerFromContext(ctx)
	if caller.TenantID == 0 || caller.UserID == "" || !caller.Role.IsValid() {
		return nil, fmt.Errorf("结构化能力读取需要已核验的用户和空间")
	}
	set := t.registry.structuredCapabilities
	if !request.Refresh {
		// A registry may be created after planning chose another source channel.
		// Inspecting its capabilities must not report a service outage merely
		// because the turn has never negotiated. Only this explicit capability
		// request initializes protocol schemas; it never reads source data.
		if set.manifest != "" || set.refreshes > 0 {
			return t.registry.structuredCapabilityResult(), nil
		}
		if result, hit := t.registry.restoreStructuredManifest(); hit {
			return result, nil
		}
	}
	if set.refreshes >= 3 {
		return structuredProtocolFailure("manifest_refresh_budget_exceeded", "本轮能力协商已达到预算；请使用实际仍可用的能力，或说明本次协议服务未就绪，不能推断无数据", "manifest", false), nil
	}
	set.refreshes++
	ctx, cancel := context.WithTimeout(ctx, StructuredManifestTimeout)
	defer cancel()
	arguments, _ := json.Marshal(map[string]any{"operation": "describe_tools", "arguments": map[string]any{"operations": StructuredOperationNames()}})
	result, err := t.registry.structuredCapabilities.base.Execute(ctx, arguments)
	if err != nil || result == nil || !result.Success {
		t.registry.structuredCapabilities.lastError = "manifest_unavailable"
		return structuredProtocolFailure("manifest_unavailable", "结构化能力协商未完成；其他已授权能力仍可使用，可在预算内 refresh=true 重试。此状态不表示无数据", "manifest", false), nil
	}
	installed, err := t.registry.InstallStructuredManifest(result.Data)
	if installed != nil && !installed.Success {
		set.lastError, _ = installed.Data["code"].(string)
	}
	return installed, err
}

// The cache contains only versioned, service-wide protocol schemas, never a
// caller, scope directory or source result. Execution still pins the service
// version and checks its response, so a deploy cannot silently reinterpret it.
var structuredManifestCache = struct {
	sync.Mutex
	entries map[string]structuredManifestEntry
}{entries: map[string]structuredManifestEntry{}}

type structuredManifestEntry struct {
	raw []byte
	at  time.Time
}

func structuredManifestServiceKey(base *StructuredDataTool) string {
	return structuredDigest([]any{base.endpoint, hex.EncodeToString(base.key)})
}

func rememberStructuredManifest(base *StructuredDataTool, data map[string]any) {
	raw, err := json.Marshal(data)
	if err != nil || len(raw) > 256*1024 {
		return
	}
	cache := &structuredManifestCache
	cache.Lock()
	defer cache.Unlock()
	if len(cache.entries) >= 8 {
		for key := range cache.entries {
			delete(cache.entries, key)
			break
		}
	}
	cache.entries[structuredManifestServiceKey(base)] = structuredManifestEntry{raw: raw, at: time.Now()}
}

func (r *ToolRegistry) BeginStructuredProtocolTurn(ctx context.Context) (*types.ToolResult, bool) {
	if !r.PrepareStructuredProtocol() {
		return nil, false
	}
	set := r.structuredCapabilities
	set.refreshes = 0
	caller := types.CallerFromContext(ctx)
	if caller.TenantID == 0 || caller.UserID == "" || !caller.Role.IsValid() {
		return nil, false
	}
	// Unversioned schemas must not survive a failed negotiation in a new turn.
	// Drain is guaranteed by the caller's turn/refresh execution boundary.
	set.base.protocolMu.Lock()
	set.generation++
	set.base.protocolGeneration = set.generation
	for name, owned := range set.owned {
		if r.tools[name] == owned {
			delete(r.tools, name)
			delete(r.deferred, name)
		}
	}
	set.owned = map[string]*structuredReadOperationTool{}
	set.base.protocolDescriptors = map[string]StructuredOperationDescriptor{}
	set.manifest, set.protocol, set.lastError = "", "", ""
	set.base.protocolMu.Unlock()
	return r.restoreStructuredManifest()
}

// restoreStructuredManifest shares only authenticated service schemas. Caller
// authentication is checked by both entrypoints before consulting this cache;
// source permissions and version pins are still checked by every data call.
func (r *ToolRegistry) restoreStructuredManifest() (*types.ToolResult, bool) {
	set := r.structuredCapabilities
	cache := &structuredManifestCache
	cache.Lock()
	entry, found := cache.entries[structuredManifestServiceKey(set.base)]
	cache.Unlock()
	if !found || time.Since(entry.at) > 10*time.Minute {
		return nil, false
	}
	var data map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(entry.raw)))
	decoder.UseNumber()
	if decoder.Decode(&data) != nil {
		return nil, false
	}
	result, err := r.InstallStructuredManifest(data)
	return result, err == nil && result != nil && result.Success
}

func structuredProtocolFailure(code, message, stage string, started bool) *types.ToolResult {
	data := map[string]any{"kind": "structured_protocol_error", "status": "error", "code": code, "reason_code": code, "stage": stage, "retryable": code == "manifest_unavailable" || code == "manifest_changed", "execution_started": started, "manifest_version": ""}
	encoded, _ := json.Marshal(data)
	return &types.ToolResult{Success: false, Error: message, Data: data, Output: string(encoded)}
}
