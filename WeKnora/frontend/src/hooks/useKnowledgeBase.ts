import { ref, reactive, onBeforeUnmount, watch } from "vue";
import { storeToRefs } from "pinia";
import { formatStringDate } from "../utils/index";
import {
  listKnowledgeFiles,
  getKnowledgeDetails,
  getKnowledgeDetailsCon,
  type ListKnowledgeFilesParams,
} from "@/api/knowledge-base/index";
import { knowledgeStore } from "@/stores/knowledge";
import { useRoute } from 'vue-router';
import { useI18n } from 'vue-i18n';
import { useAuthStore } from '@/stores/auth';

export default function (knowledgeBaseId?: string) {
  const usemenuStore = knowledgeStore();
  const route = useRoute();
  const auth = useAuthStore();
  const { t } = useI18n();
  const { cardList, total } = storeToRefs(usemenuStore);
  let moreIndex = ref(-1);
  const details = reactive({
    title: "",
    time: "",
    md: [] as any[],
    id: "",
    total: 0,
    type: "",
    source: "",
    channel: "",
    file_type: "",
    description: "",
    summary_status: "",
    parse_status: "",
    error_message: "",
	custom_metadata: {} as Record<string, unknown>,
    chunkLoading: false,
    chunkLoadError: "",
    tags: [] as Array<{ id: string; name: string; color?: string }>,
  });
  let knowledgeListGeneration = 0;
  let detailRequestGeneration = 0;
  let chunkRequestGeneration = 0;
  let activeKnowledgeId = '';
  let mounted = true;
  const currentKbId = () => String(route.params.kbId || knowledgeBaseId || '');
  const currentScope = () => JSON.stringify([currentKbId(), auth.user?.id || '', auth.effectiveTenantId || '']);
  const scopeCurrent = (scope: string) => mounted && scope === currentScope();
  watch(currentScope, () => {
    knowledgeListGeneration++;
    detailRequestGeneration++;
    chunkRequestGeneration++;
    activeKnowledgeId = '';
    cardList.value = [];
    total.value = 0;
    Object.assign(details, {
      id: '', title: '', time: '', md: [], total: 0, type: '', source: '', channel: '', file_type: '',
      description: '', summary_status: '', parse_status: '', error_message: '', tags: [], custom_metadata: {},
      chunkLoading: false, chunkLoadError: '',
    });
  });
  onBeforeUnmount(() => { mounted = false; });
  const getKnowled = (
    query: ListKnowledgeFilesParams = { page: 1, page_size: 35 },
    kbId?: string,
  ): Promise<void> => {
    const targetKbId = kbId || knowledgeBaseId;
    if (!targetKbId) return Promise.resolve();
    const scope = currentScope();
    const requestGeneration = query.page === 1 ? ++knowledgeListGeneration : knowledgeListGeneration;

    return listKnowledgeFiles(targetKbId, query)
      .then((result: any) => {
        if (requestGeneration !== knowledgeListGeneration || !scopeCurrent(scope)) return;

        const currentRouteKbId = (route.params as any)?.kbId as string | undefined;
        if (currentRouteKbId && currentRouteKbId !== targetKbId) return;

        const { data, total: totalResult } = result;
    const cardList_ = data.map((item: any) => {
      const rawName = item.file_name || item.title || item.source || t('knowledgeBase.untitledDocument')
      const dotIndex = rawName.lastIndexOf('.')
      const displayName = dotIndex > 0 ? rawName.substring(0, dotIndex) : rawName
      const fileTypeSource = item.file_type || (item.type === 'manual' ? 'MANUAL' : '')
      return {
        ...item,
        original_file_name: item.file_name,
        display_name: displayName,
        file_name: displayName,
        folder_path: item.folder_path || '',
        updated_at: formatStringDate(new Date(item.updated_at)),
        isMore: false,
        file_type: fileTypeSource ? String(fileTypeSource).toLocaleUpperCase() : '',
      }
    });
        
        if (query.page === 1) {
          cardList.value = cardList_;
        } else {
          cardList.value.push(...cardList_);
        }
        total.value = totalResult;
      })
      .catch(() => {});
  };
  const openMore = (index: number) => {
    moreIndex.value = index;
  };
  const onVisibleChange = (visible: boolean) => {
    if (!visible) {
      moreIndex.value = -1;
    }
  };
  const getCardDetails = (item: any) => {
    activeKnowledgeId = item.id;
    const scope = currentScope();
    const targetKbId = currentKbId();
    const requestGeneration = ++detailRequestGeneration;
    chunkRequestGeneration++;
    Object.assign(details, {
      title: "",
      time: "",
      md: [],
      id: "",
      type: "",
      source: "",
      channel: "",
      file_type: "",
      description: "",
      summary_status: "",
      parse_status: "",
      error_message: "",
	  custom_metadata: {},
      chunkLoadError: "",
      chunkLoading: true,
      tags: item?.tags ? [...item.tags] : [],
    });
    getKnowledgeDetails(item.id)
      .then((result: any) => {
        if (requestGeneration !== detailRequestGeneration || activeKnowledgeId !== item.id || !scopeCurrent(scope)) return;
        if (result.success && result.data) {
          const { data } = result;
          if (data.id !== item.id || data.knowledge_base_id !== targetKbId) {
            details.chunkLoading = false;
            details.chunkLoadError = t('knowledgeBase.chunkLoadFailed');
            return;
          }
          Object.assign(details, {
            title: data.file_name || data.title || data.source || t('knowledgeBase.untitledDocument'),
            time: formatStringDate(new Date(data.updated_at)),
            id: data.id,
            type: data.type || 'file',
            source: data.source || '',
            channel: data.channel || '',
            file_type: data.file_type || '',
            description: data.description || '',
            summary_status: data.summary_status || '',
            parse_status: data.parse_status || '',
            error_message: data.error_message || '',
			custom_metadata: data.custom_metadata || {},
            tags: data.tags?.length ? data.tags : (item?.tags || []),
          });
          getfDetails(item.id, 1);
        } else {
          details.chunkLoading = false;
          details.chunkLoadError = result?.message || result?.error?.message || t('knowledgeBase.chunkLoadFailed');
        }
      })
      .catch((error: any) => {
        if (requestGeneration !== detailRequestGeneration || !scopeCurrent(scope)) return;
        details.chunkLoading = false;
        details.chunkLoadError = error?.message || t('knowledgeBase.chunkLoadFailed');
      });
  };
  
  const getfDetails = (id: string, page: number) => {
    if (id !== activeKnowledgeId || details.id !== id) return;
    const scope = currentScope();
    const requestGeneration = ++chunkRequestGeneration;
    details.chunkLoading = true;
    details.chunkLoadError = "";
    getKnowledgeDetailsCon(id, page)
      .then((result: any) => {
        if (requestGeneration !== chunkRequestGeneration || activeKnowledgeId !== id || !scopeCurrent(scope)) return;
        if (result.success && result.data) {
          const { data, total: totalResult } = result;
          details.md = data;
          details.total = totalResult;
        } else {
          details.chunkLoadError = result?.message || result?.error?.message || t('knowledgeBase.chunkLoadFailed');
        }
      })
      .catch((err: any) => {
        if (requestGeneration !== chunkRequestGeneration || activeKnowledgeId !== id || !scopeCurrent(scope)) return;
        details.chunkLoadError = err?.message || t('knowledgeBase.chunkLoadFailed');
        console.error("[ChunkLoad] failed", {
          knowledgeId: id,
          page,
          error: err,
        });
      })
      .finally(() => {
        if (requestGeneration === chunkRequestGeneration && scopeCurrent(scope)) {
          details.chunkLoading = false;
        }
      });
  };
  return {
    cardList,
    moreIndex,
    getKnowled,
    details,
    openMore,
    onVisibleChange,
    getCardDetails,
    total,
    getfDetails,
  };
}
