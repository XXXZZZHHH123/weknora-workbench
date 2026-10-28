// User feedback (bug reports and suggestions). Submissions are scoped to the
// signed-in user by the backend; the list endpoints are system-admin only.

import { get, post, put } from '@/utils/request'

export type FeedbackKind = 'bug' | 'suggestion'
export type FeedbackStatus = 'open' | 'resolved'

export interface FeedbackEntry {
  id: number
  tenant_id: number
  user_id: string
  kind: FeedbackKind
  content: string
  page_url: string
  page_title: string
  user_agent: string
  status: FeedbackStatus
  created_at: string
  updated_at: string
  username?: string
  email?: string
  tenant_name?: string
}

export interface FeedbackPage {
  items: FeedbackEntry[]
  next_cursor: number
  open_count: number
}

export interface SubmitFeedbackInput {
  kind: FeedbackKind
  content: string
  page_url?: string
  page_title?: string
}

export function submitFeedback(input: SubmitFeedbackInput) {
  return post<{ success: boolean; data: FeedbackEntry }>('/api/v1/feedback', input)
}

export interface ListFeedbackParams {
  status?: FeedbackStatus | ''
  kind?: FeedbackKind | ''
  after_id?: number
  limit?: number
}

export async function listFeedback(params: ListFeedbackParams = {}): Promise<FeedbackPage> {
  const qs = new URLSearchParams()
  if (params.status) qs.append('status', params.status)
  if (params.kind) qs.append('kind', params.kind)
  if (params.after_id) qs.append('after_id', String(params.after_id))
  if (params.limit) qs.append('limit', String(params.limit))
  const tail = qs.toString()
  const res = (await get(`/api/v1/system/admin/feedback${tail ? '?' + tail : ''}`)) as unknown as {
    data: FeedbackPage
  }
  return res.data
}

export function setFeedbackStatus(id: number, status: FeedbackStatus) {
  return put(`/api/v1/system/admin/feedback/${id}/status`, { status })
}
