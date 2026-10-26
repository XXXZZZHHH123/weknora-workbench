import { get, post, put, del } from '@/utils/request'
import type { SourceSharingModule } from '@/utils/sourceSharingModules'

export interface AssistantProject {
  id: string
  name: string
  description?: string
  knowledge_base_id?: string
  session_count: number
  created_at?: string
  updated_at?: string
}

export type ResearchWorkbenchTemplateId = 'industrial' | 'data-tracking' | 'topic' | 'blank'
export interface ResearchWorkbenchOnboarding {
  status: 'active' | 'dismissed' | 'skipped' | 'completed'
  tasks: { sources: boolean; review: boolean; research: boolean }
}
export interface ResearchWorkbenchTemplate {
  id: ResearchWorkbenchTemplateId
  name: string
  version: number
  modules: string[]
}

export interface ResearchWorkbench {
  scope_version?: 2
  sources_revision?: number
  sources?: ResearchWorkbenchSource[]
  is_pinned?: boolean
  pinned_at?: string | null
  owner_id?: string
  can_manage?: boolean
  can_manage_permissions?: boolean
  can_edit?: boolean
  can_manage_source?: boolean
  can_read_source?: boolean
  can_edit_source?: boolean
  source_data_available?: boolean
  source_sharing_approved?: boolean
  source_sharing_modules?: SourceSharingModule[]
  creator_id?: string
  id: string
  name: string
  description?: string
  knowledge_base_id: string
  knowledge_base_name?: string
  scope_tenant_id: number
  workspace_mode: 'legacy' | 'isolated'
  template_id: ResearchWorkbenchTemplateId | ''
  template_version: number
  onboarding: ResearchWorkbenchOnboarding
  created_at?: string
  updated_at?: string
}

export interface ProjectSession {
  id: string
  title: string
  description?: string
  is_pinned?: boolean
  created_at?: string
  updated_at?: string
}

type Envelope<T> = { success: boolean; data: T; message?: string }
type ProjectInput = { name: string; description?: string; knowledge_base_id?: string }
export type WorkbenchSourceInput = { source_type: 'knowledge_base' | 'central_dataset'; source_id: string }
export interface ResearchWorkbenchSource extends WorkbenchSourceInput {
  id: string
  name?: string
  knowledge_base_id?: string
  tenant_id?: number
  status: string
  can_read: boolean
  can_manage_source: boolean
  can_edit?: boolean
  delegated: boolean
  view_modules: SourceSharingModule[]
  sharing_approved?: boolean
  sharing_modules?: SourceSharingModule[]
  sharing_requested?: boolean
}
export interface CentralDataset {
  id: string
  kind: string
  name: string
  knowledge_base_id: string
  created_at?: string
}
export interface WorkbenchSourcesView {
  sources_revision: number
  sources: ResearchWorkbenchSource[]
}
type WorkbenchInput = { name: string; description?: string; knowledge_base_id?: string; sources?: WorkbenchSourceInput[] }
type WorkbenchCreateInput = WorkbenchInput & { template_id?: ResearchWorkbenchTemplateId; template_version?: number; idempotency_key: string }
export type ResearchWorkbenchOnboardingInput = {
  status?: ResearchWorkbenchOnboarding['status']
  tasks?: Partial<ResearchWorkbenchOnboarding['tasks']>
}
const base = '/api/v1/workspaces'
const id = encodeURIComponent

export interface DefaultKnowledgeBaseBinding {
  kind: 'macro'
  knowledge_base_id: string
  name: string
  source_scope: 'knowledge_base'
}

export function getDefaultKnowledgeBase(): Promise<Envelope<DefaultKnowledgeBaseBinding | null>> {
  return get(`${base}/default-knowledge-base`)
}
export function initializeDefaultKnowledgeBase(): Promise<Envelope<DefaultKnowledgeBaseBinding>> {
  return post(`${base}/default-knowledge-base`, {})
}
export function listCentralDatasets(): Promise<Envelope<CentralDataset[]>> {
  return get(`${base}/central-datasets`)
}
export function initializeCentralDataset(): Promise<Envelope<CentralDataset>> {
  return post(`${base}/central-datasets/default`, {})
}
export function getResearchWorkbenchSources(workbenchId: string): Promise<Envelope<WorkbenchSourcesView>> {
  return get(`${base}/research/${id(workbenchId)}/sources`)
}
export function updateResearchWorkbenchSources(workbenchId: string, expectedRevision: number, sources: WorkbenchSourceInput[]): Promise<Envelope<WorkbenchSourcesView>> {
  return put(`${base}/research/${id(workbenchId)}/sources`, { expected_revision: expectedRevision, sources })
}
export function updateResearchWorkbenchSourceSharing(workbenchId: string, bindingId: string, approved: boolean, modules: SourceSharingModule[]): Promise<Envelope<{ approved: boolean; source_sharing_modules: SourceSharingModule[]; can_manage_source: boolean }>> {
  return put(`${base}/research/${id(workbenchId)}/sources/${id(bindingId)}/sharing`, { approved, modules })
}
export function requestResearchWorkbenchSourceSharing(workbenchId: string, bindingId: string): Promise<Envelope<{ requested: boolean }>> {
  return post(`${base}/research/${id(workbenchId)}/sources/${id(bindingId)}/sharing/request`, {})
}

export function listAssistantProjects(): Promise<Envelope<AssistantProject[]>> {
  return get(`${base}/projects`)
}
export function getAssistantProject(projectId: string): Promise<Envelope<AssistantProject>> {
  return get(`${base}/projects/${id(projectId)}`)
}
export function createAssistantProject(data: ProjectInput): Promise<Envelope<AssistantProject>> {
  return post(`${base}/projects`, data)
}
export function updateAssistantProject(projectId: string, data: Partial<ProjectInput>): Promise<Envelope<AssistantProject>> {
  return put(`${base}/projects/${id(projectId)}`, data)
}
export function deleteAssistantProject(projectId: string) {
  return del(`${base}/projects/${id(projectId)}`)
}
export function listProjectSessions(projectId: string, page = 1, page_size = 20): Promise<Envelope<ProjectSession[]> & { total: number }> {
  return get(`${base}/projects/${id(projectId)}/sessions`, { params: { page, page_size } })
}
export function assignSessionToProject(projectId: string, sessionId: string) {
  return put(`${base}/projects/${id(projectId)}/sessions/${id(sessionId)}`, {})
}
export function removeSessionFromProject(projectId: string, sessionId: string) {
  return del(`${base}/projects/${id(projectId)}/sessions/${id(sessionId)}`)
}
export function listResearchWorkbenches(params?: { knowledge_base_id?: string }): Promise<Envelope<ResearchWorkbench[]>> {
  return get(`${base}/research`, { params })
}
export function getResearchWorkbench(workbenchId: string): Promise<Envelope<ResearchWorkbench>> {
  return get(`${base}/research/${id(workbenchId)}`)
}
export function createResearchWorkbench(data: WorkbenchCreateInput): Promise<Envelope<ResearchWorkbench>> {
  return post(`${base}/research`, data)
}
export function updateResearchWorkbench(workbenchId: string, data: Partial<WorkbenchInput>): Promise<Envelope<ResearchWorkbench>> {
  return put(`${base}/research/${id(workbenchId)}`, data)
}
export function deleteResearchWorkbench(workbenchId: string): Promise<Envelope<null>> {
  return del(`${base}/research/${id(workbenchId)}`)
}

export function listResearchWorkbenchTemplates(): Promise<Envelope<ResearchWorkbenchTemplate[]>> {
  return get(`${base}/research/templates`)
}
export function updateResearchWorkbenchOnboarding(workbenchId: string, data: ResearchWorkbenchOnboardingInput): Promise<Envelope<ResearchWorkbench>> {
  return put(`${base}/research/${id(workbenchId)}/onboarding`, data)
}

export function setResearchWorkbenchPin(workbenchId: string, isPinned: boolean): Promise<Envelope<{id: string; is_pinned: boolean; pinned_at: string | null}>> {
  return put(`${base}/research/${id(workbenchId)}/pin`, { is_pinned: isPinned })
}
