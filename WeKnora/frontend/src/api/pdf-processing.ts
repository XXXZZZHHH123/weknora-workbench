import { get, put } from '@/utils/request'

export interface PDFProcessingSettings {
  overlap_pages: number
  grouping_request_bytes: number
  queue_timeout_seconds: number
  execution_timeout_seconds: number
}
export interface PDFProcessingSettingsResponse {
  data: PDFProcessingSettings
  defaults: PDFProcessingSettings
  can_edit: boolean
}
const path = '/api/v1/tenants/kv/pdf-processing-settings'
export const getPDFProcessingSettings = (): Promise<PDFProcessingSettingsResponse> => get(path)
export const updatePDFProcessingSettings = (settings: PDFProcessingSettings): Promise<PDFProcessingSettingsResponse> => put(path, settings)
