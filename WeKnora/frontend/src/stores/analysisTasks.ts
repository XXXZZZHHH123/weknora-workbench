import { defineStore } from 'pinia'
import { useAuthStore } from './auth'
import { dismissAllAnalysisJobs, dismissAnalysisJob, listAnalysisJobs } from '../api/researchAnalysisTasks'
import { createAnalysisTasks } from './analysisTasksState'

export const useAnalysisTasksStore = defineStore('researchAnalysisTasks', () => {
  const auth = useAuthStore()
  let storage: Storage | undefined
  try { storage = localStorage } catch { /* Task restoration can use the server alone. */ }
  return createAnalysisTasks({
    owner: () => auth.user?.id && auth.effectiveTenantId && auth.token
      ? { userId: auth.user.id, tenantId: String(auth.effectiveTenantId), token: auth.token } : null,
    fetchJobs: listAnalysisJobs, dismissJob: dismissAnalysisJob, dismissAllJobs: dismissAllAnalysisJobs, storage,
    isVisible: () => typeof document === 'undefined' || document.visibilityState !== 'hidden',
  })
})
