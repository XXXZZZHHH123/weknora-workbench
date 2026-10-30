// @ts-nocheck
<script setup lang="ts">
import { indexEmbeddedSourceImages } from '@/utils/sourceImage';
import { ref, shallowRef, watch, onMounted, onUnmounted, onErrorCaptured, nextTick, defineAsyncComponent } from 'vue';
import { previewKnowledgeFile, getKnowledgeDetails } from '@/api/knowledge-base/index';
import { previewKnowledgeMedia } from '@/api/knowledge-base/preview-media';
import { previewTemporaryAttachment } from '@/api/chat/temporary-attachments';
import { downloadArtifact } from '@/api/chat';
import hljs from 'highlight.js';
import 'highlight.js/styles/github.css';
import 'katex/dist/katex.min.css';
import { useI18n } from 'vue-i18n';
import { sanitizeHTML, sanitizeMarkdownHTML, hydrateProtectedFileImages } from '@/utils/security';
import { preparePptxPreview, isCompletePptxRender, watchCompletePptxSlideDOM, bindPptxRendererCallbacks, isPptxSourceKeyCurrent, resolvePptxSlideLocator, resolvePptxLocatePage, type PptxRendererCallbacks } from '@/utils/pptxPreview';
import { preparePptxOlePreview, sha256Hex } from '@/utils/pptxMedia';
import { useAuthStore } from '@/stores/auth';
import { getPptxPreviewScope, pptxPreviewCache } from '@/utils/pptxPreviewCache';
import { knowledgePptxSourceKey } from '@/utils/pptxPreviewSource';
import { createPptxViewportMedia, type PptxPageMediaView } from '@/utils/pptxViewportMedia';
import { findMarkdownSourceRange } from '@/utils/markdownSourceLocate';
import { renderDocumentPreviewMarkdown } from '@/utils/documentPreviewMarkdown';
import { buildHtmlPreview } from '@/utils/htmlPreview';
import { openMermaidFullscreen } from '@/utils/mermaidViewer';
import { renderMermaidToSvg } from '@/utils/mermaidShared';
import {
  FILE_PREVIEW_SNIFF_BYTES,
  getHighlightLang as resolveHighlightLang,
  getPreviewMimeType,
  prettyPrintJson,
  resolveFilePreviewExt,
  resolvePreviewKind,
  shouldPrettyPrintJson,
  sniffPreview,
  type FilePreviewKind,
} from '@/utils/filePreview';
import {
  findInText,
  sourceQuoteText,
  type SourceLocateRequest,
} from '@/utils/sourceLocator';
import {
  buildTextIndex,
  findTextRange,
  highlightElements,
  highlightRanges,
  rangeForOffsets,
  scrollRectIntoContainer,
} from '@/utils/sourceLocatorDom';
import { renderEpubPreview } from '@/utils/epubPreview';


const VueOfficePptx = defineAsyncComponent(() => import('@vue-office/pptx'));
const PdfSourceViewer = defineAsyncComponent(() => import('@/components/source-preview/PdfSourceViewer.vue'));
const ExcelSourceViewer = defineAsyncComponent(() => import('@/components/source-preview/ExcelSourceViewer.vue'));

const { t } = useI18n();
const previewAuth = useAuthStore();

const props = defineProps<{
  sourceBlob?: Blob;
  knowledgeId?: string;
  sessionId?: string;
  attachmentId?: string;
  messageId?: string;
  artifactIndex?: number;
  fileType: string;
  fileName: string;
  active: boolean;
  fillHeight?: boolean;
  /** Place preview actions beside the host header's download button. */
  toolbarTarget?: HTMLElement | null;
  /**
   * Source mode renders PDFs with pdf.js instead of the browser viewer so a
   * citation can be scrolled to and highlighted.
   */
  sourceMode?: boolean;
  /** Where to scroll and what to highlight once the file is rendered. */
  locate?: SourceLocateRequest | null;
}>();

export type SourceLocateResult = { found: boolean; precise: boolean; granularity?: string; reason?: string; partial?: boolean; page?: number };

const emit = defineEmits<{
  located: [result: SourceLocateResult];
}>();

const loading = ref(false);
const pptxPermissionChecking = ref(false);
let previewLoadVersion = 0;
let previewSecurityScope = getPptxPreviewScope();
let loadedPptxAuthority = '';
let pptxMediaCacheContext: { scope: string; sourceKey: string } | undefined;
const error = ref('');
// Async renderer setup/mount failures do not emit the renderer's `error` event.
// Surface them in the preview instead of leaving an empty document area.
onErrorCaptured((err) => {
  error.value = t('preview.loadFailed');
  loading.value = false;
  console.error('Document renderer failed:', err);
  return false;
});
const previewType = ref<FilePreviewKind>('unsupported');
const blobUrl = ref('');
const textContent = ref('');
const highlightedCode = ref('');
const markdownHtml = ref('');
const excelBlob = shallowRef<Blob | null>(null);
const mermaidSvg = ref('');
const htmlViewMode = ref<'render' | 'source'>('render');
const pptxData = shallowRef<ArrayBuffer | null>(null);
const pptxRenderedData = shallowRef<ArrayBuffer | null>(null);
const pptxRendererKey = ref('');
let pptxRenderGeneration = 0;
const pptxInlineMediaFailed = ref(false);
const pdfData = shallowRef<ArrayBuffer | null>(null);
const epubHtml = ref('');
let epubObjectUrls: string[] = [];
let previewExt = '';
let pptxSlideCount = 0;
let pptxSourceSha256 = '';
let pptxSourceHashError = '';
const pptxRendererCallbacks = shallowRef<PptxRendererCallbacks | null>(null);
const PPTX_RENDER_TIMEOUT_MS = 45_000;
let cancelPptxRenderWatch: (() => void) | null = null;
let pptxRenderSourceKey = '';
let pptxRenderLocateToken: number | null = null;
let pptxRenderReadySourceKey = '';
let pptxRenderFailureSourceKey = '';
let pptxRenderFailureReason = '';
let pptxRenderSignal: 'pending' | 'complete' | 'incomplete' = 'pending';
const pptxPageMedia = ref<PptxPageMediaView[]>([]);
const pptxPageMediaState = ref<'idle' | 'discovering' | 'loading' | 'loaded' | 'partial' | 'failed' | 'none'>('idle');
let pptxViewportMedia: ReturnType<typeof createPptxViewportMedia> | null = null;
let pptxMediaSourceKey = '';
let pptxMediaGeneration = 0;
const pptxMediaPages = new Map<number, PptxPageMediaView[]>();

function stopPptxRenderWatch() {
  cancelPptxRenderWatch?.();
  cancelPptxRenderWatch = null;
  pptxRenderSourceKey = '';
  pptxRenderLocateToken = null;
}

function exactPptxLocatePage(): number {
  if (!props.sourceMode || !props.knowledgeId || !props.active) return 0;
  return resolvePptxLocatePage(props.locate?.locators || [], pptxSlideCount, !props.locate?.unavailable);
}

function clearPptxPageMedia() {
  pptxViewportMedia?.stop();
  pptxViewportMedia = null;
  pptxMediaSourceKey = '';
  pptxMediaPages.clear();
  pptxPageMedia.value = [];
  pptxPageMediaState.value = 'idle';
  pptxInlineMediaFailed.value = false;
}

async function setPptxRendererData(data: ArrayBuffer, sourceKey: string) {
  stopPptxRenderWatch();
  pptxRenderReadySourceKey = '';
  pptxRenderFailureSourceKey = '';
  pptxRenderFailureReason = '';
  const renderKey = `${sourceKey}|${++pptxRenderGeneration}`;
  pptxRendererKey.value = renderKey;
  pptxRendererCallbacks.value = bindPptxRendererCallbacks(sourceKey, {
    onRendered: (key, result) => { if (pptxRendererKey.value === renderKey) onPptxRendered(key, result); },
    onError: (key, reason) => { if (pptxRendererKey.value === renderKey) onPptxError(key, reason); },
  });
  pptxRenderedData.value = data;
  await nextTick();
  if (pptxRendererKey.value === renderKey && isCurrentPptxSource(sourceKey)) watchPptxRender(sourceKey);
}

function updatePptxPageMediaState() {
  const rows = pptxPageMedia.value;
  if (!rows.length) { pptxPageMediaState.value = 'none'; return; }
  const failures = rows.filter((item) => item.state === 'failed').length;
  const displayed = rows.filter((item) => item.state === 'displayed').length;
  const pending = rows.some((item) => item.state === 'pending' || item.state === 'loading' || item.state === 'loaded');
  if (pending) pptxPageMediaState.value = 'loading';
  else if (failures && displayed) pptxPageMediaState.value = 'partial';
  else if (failures) pptxPageMediaState.value = 'failed';
  else pptxPageMediaState.value = 'loaded';
}

async function loadPptxPageMedia(force = false) {
  const sourceKey = loadedForId;
  if (!sourceKey || !props.active || pptxPermissionChecking.value || !props.knowledgeId ||
      props.locate?.unavailable || previewType.value !== 'pptx' || !pptxData.value) {
    clearPptxPageMedia();
    return;
  }
  if (!pptxSourceSha256) {
    clearPptxPageMedia();
    pptxPageMediaState.value = 'failed';
    return;
  }
  const mediaKey = `${sourceKey}|${pptxSourceSha256}`;
  if (!pptxViewportMedia || pptxMediaSourceKey !== mediaKey) {
    clearPptxPageMedia();
    pptxMediaSourceKey = mediaKey;
    const knowledgeId = props.knowledgeId;
    const sourceSha256 = pptxSourceSha256;
    const loader = createPptxViewportMedia({
      cache: pptxMediaCacheContext,
      data: pptxData.value, sourceSha256, generation: ++pptxMediaGeneration,
      loadRaster: (page, part, signal) => previewKnowledgeMedia(knowledgeId, page, part, sourceSha256, signal),
      onPage: (page, items) => {
        if (pptxViewportMedia !== loader || !isCurrentPptxSource(sourceKey)) return;
        pptxMediaPages.set(page, items);
        if (page === exactPptxLocatePage()) {
          pptxPageMedia.value = items;
          updatePptxPageMediaState();
        }
      },
      onPageError: (page) => {
        if (pptxViewportMedia === loader && isCurrentPptxSource(sourceKey) && page === exactPptxLocatePage()) pptxPageMediaState.value = 'failed';
      },
    });
    pptxViewportMedia = loader;
    if (previewContent.value) loader.observe(previewContent.value);
  }
  const page = exactPptxLocatePage();
  if (page) {
    pptxPageMedia.value = pptxMediaPages.get(page) || [];
    pptxPageMediaState.value = pptxMediaPages.has(page) ? 'none' : 'discovering';
    if (pptxMediaPages.has(page)) updatePptxPageMediaState();
    await pptxViewportMedia.loadPage(page, force);
  }
}

function onPptxMediaImageLoad(part: string, generation: number) {
  if (generation === pptxMediaGeneration) pptxViewportMedia?.markDisplayed(part);
}

function onPptxMediaImageError(part: string, generation: number) {
  if (generation === pptxMediaGeneration) pptxViewportMedia?.markFailed(part);
}

function retryPptxPageMedia() {
  if (!pptxSourceHashError) void loadPptxPageMedia(true);
}

function isCurrentPptxSource(sourceKey: string) {
  return !pptxPermissionChecking.value && isPptxSourceKeyCurrent(sourceKey, loadedForId, props.active) && previewType.value === 'pptx';
}

function failPptxRender(sourceKey: string, reason: string, detail?: string, token = props.locate?.token ?? null) {
  if (!isCurrentPptxSource(sourceKey)) return;
  error.value = detail || t('preview.loadFailed');
  stopPptxRenderWatch();
  pptxRenderFailureSourceKey = sourceKey;
  pptxRenderFailureReason = reason;
  if (props.locate && props.locate.token === token) emit('located', { found: false, precise: false, reason });
}

function watchPptxRender(sourceKey: string) {
  if (!sourceKey || !props.active || loadedForId !== sourceKey || previewType.value !== 'pptx') return;
  if (pptxRenderReadySourceKey === sourceKey) {
    if (props.locate) scheduleLocate();
    return;
  }
  if (pptxRenderFailureSourceKey === sourceKey) {
    if (props.locate) emit('located', { found: false, precise: false, reason: pptxRenderFailureReason || 'render_failed' });
    return;
  }
  const token = props.locate?.token ?? null;
  if (pptxRenderSourceKey !== sourceKey || pptxRenderLocateToken !== token) {
    stopPptxRenderWatch();
    pptxRenderSourceKey = sourceKey;
    pptxRenderLocateToken = token;
    pptxRenderSignal = 'pending';
    const root = previewContent.value;
    if (!root) {
      pptxRenderSourceKey = '';
      pptxRenderLocateToken = null;
      return;
    }
    cancelPptxRenderWatch = watchCompletePptxSlideDOM(root, pptxSlideCount, {
      onReady: () => {
        if (!isCurrentPptxSource(sourceKey) || (props.locate?.token ?? null) !== token) return;
        pptxRenderReadySourceKey = sourceKey;
        stopPptxRenderWatch();
        if (props.locate) scheduleLocate();
      },
      onTimeout: () => {
        if (!isCurrentPptxSource(sourceKey) || (props.locate?.token ?? null) !== token) return;
        const reason = pptxRenderSignal === 'incomplete' ? 'render_incomplete' : 'render_timeout';
        failPptxRender(sourceKey, reason, undefined, token);
      },
    }, { timeoutMs: PPTX_RENDER_TIMEOUT_MS });
  }
}

function onPptxRendered(sourceKey: string, result: unknown) {
  if (!isCurrentPptxSource(sourceKey)) return;
  watchPptxRender(sourceKey);
  if (pptxRenderSourceKey === sourceKey) {
    pptxRenderSignal = isCompletePptxRender(result, pptxSlideCount) ? 'complete' : 'incomplete';
  }
}

function onPptxError(sourceKey: string, reason: unknown) {
  if (!isCurrentPptxSource(sourceKey)) return;
  const detail = (reason as { message?: unknown } | null)?.message;
  failPptxRender(sourceKey, 'render_failed', typeof detail === 'string' && detail ? detail : undefined);
}
const docxContainer = ref<HTMLElement | null>(null);
const imageNaturalWidth = ref(0);
const imageNaturalHeight = ref(0);
let loadedForId = '';

const isFullscreen = ref(false);
const previewRoot = ref<HTMLElement | null>(null);
const previewContent = ref<HTMLElement | null>(null);

function focusPreviewContent() {
  const root = previewRoot.value;
  if (!props.active || !root?.isConnected) return;
  // Focus the actual scrolling element so the browser handles arrows, Space,
  // PageUp/Down and Home/End, including native iframe and media controls.
  const target = previewContent.value || docxContainer.value || root;
  target.focus({ preventScroll: true });
}

watch(
  () => [props.active, previewRoot.value, getPreviewSourceKey(), props.sourceBlob, htmlViewMode.value, isFullscreen.value],
  async () => {
    if (!props.active) return;
    const previousFocus = document.activeElement;
    await nextTick();
    // Do not override a user who moved to another control while rendering.
    if (document.activeElement !== previousFocus && document.activeElement !== document.body) return;
    focusPreviewContent();
  },
  { flush: 'post' },
);

watch(
  () => [previewContent.value, docxContainer.value],
  () => {
    // While downloading, focus rests on the preview root. Transfer it only
    // if it is still there when the content arrives (never steal input focus).
    if (document.activeElement === previewRoot.value) focusPreviewContent();
  },
  { flush: 'post' },
);

function onPreviewFrameLoad() {
  if (document.activeElement === previewContent.value || document.activeElement === previewRoot.value) {
    focusPreviewContent();
  }
}

function toggleFullscreen() {
  if (isFullscreen.value) {
    void exitPreviewFullscreen();
  } else {
    void enterPreviewFullscreen();
  }
}

let fallbackOverflow: string | null = null;
let fullscreenDisposed = false;

async function enterPreviewFullscreen() {
  const root = previewRoot.value;
  if (!props.active || !root?.isConnected) return;
  // Native fullscreen also handles Escape when focus is inside a PDF viewer
  // or a sandboxed HTML iframe, whose keyboard events cannot bubble here.
  if (root.requestFullscreen) {
    try {
      await root.requestFullscreen();
      if (fullscreenDisposed || !props.active) {
        if (document.fullscreenElement === root) await document.exitFullscreen();
      } else {
        syncFullscreen();
      }
      return;
    } catch {
      // Embedded hosts may disallow the Fullscreen API. Keep page fullscreen
      // available there, with Escape handled in the parent document.
    }
  }
  if (fullscreenDisposed || !props.active || !root.isConnected) return;
  if (fallbackOverflow === null) fallbackOverflow = document.body.style.overflow;
  document.body.style.overflow = 'hidden';
  isFullscreen.value = true;
}

function clearFallbackFullscreen() {
  if (fallbackOverflow !== null) {
    document.body.style.overflow = fallbackOverflow;
    fallbackOverflow = null;
  }
  isFullscreen.value = false;
}

async function exitPreviewFullscreen() {
  if (previewRoot.value && document.fullscreenElement === previewRoot.value) {
    try {
      await document.exitFullscreen();
    } catch {
      // The browser may already have exited in response to Escape.
    }
    syncFullscreen();
    return;
  }
  clearFallbackFullscreen();
}

function syncFullscreen() {
  if (fallbackOverflow === null) {
    isFullscreen.value = !!previewRoot.value && document.fullscreenElement === previewRoot.value;
  }
}

function onFullscreenEscape(event: KeyboardEvent) {
  if (event.key !== 'Escape' || event.isComposing || !isFullscreen.value || !props.active) return;
  // Consume this Escape before a containing drawer can also close.
  event.preventDefault();
  event.stopImmediatePropagation();
  void exitPreviewFullscreen();
}

watch(() => props.active, (active) => {
  if (!active) {
    previewLoadVersion++;
    pptxPermissionChecking.value = false;
    stopPptxRenderWatch();
    clearPptxPageMedia();
    void exitPreviewFullscreen();
  } else if (props.knowledgeId && resolvePreviewKind(resolveFilePreviewExt(props.fileName, props.fileType)) === 'pptx') {
    // Block locator/renderer callbacks synchronously on a retained reopen;
    // loadPreview releases the gate only after this open's metadata succeeds.
    pptxPermissionChecking.value = true;
  }
}, { flush: 'sync' });

onMounted(() => {
  document.addEventListener('fullscreenchange', syncFullscreen);
  window.addEventListener('keydown', onFullscreenEscape, true);
});

onUnmounted(() => {
  fullscreenDisposed = true;
  document.removeEventListener('fullscreenchange', syncFullscreen);
  window.removeEventListener('keydown', onFullscreenEscape, true);
  void exitPreviewFullscreen();
  clearFallbackFullscreen();
});


function ensureBlobType(blob: Blob, ft: string): Blob {
  const expected = getPreviewMimeType(ft);
  if (blob.type === expected) return blob;
  return new Blob([blob], { type: expected });
}

function getHighlightLang(ft: string): string {
  return resolveHighlightLang(ft);
}

async function renderDocx(blob: Blob) {
  const { renderAsync } = await import('docx-preview');
  if (docxContainer.value) {
    docxContainer.value.innerHTML = '';
    await renderAsync(blob, docxContainer.value, undefined, {
      className: 'docx-preview-wrapper',
      inWrapper: true,
      ignoreWidth: false,
      ignoreHeight: false,
      ignoreFonts: false,
      breakPages: true,
      ignoreLastRenderedPageBreak: true,
      experimental: false,
      trimXmlDeclaration: true,
      useBase64URL: true,
    });
    if (docxContainer.value) await indexEmbeddedSourceImages(docxContainer.value);
  }
}

async function renderText(blob: Blob, fileType: string) {
  let text = await blob.text();
  if (shouldPrettyPrintJson(fileType)) {
    text = prettyPrintJson(text);
  }
  textContent.value = text;

  const lang = getHighlightLang(fileType);
  if (lang && hljs.getLanguage(lang)) {
    try {
      highlightedCode.value = hljs.highlight(text, { language: lang }).value;
      return;
    } catch { /* fallthrough */ }
  }
  const auto = hljs.highlightAuto(text);
  highlightedCode.value = auto.value;
}

async function renderMarkdown(blob: Blob) {
  const text = await blob.text();

  // 校验文本内容是否有效
  if (!text || typeof text !== 'string') {
    markdownHtml.value = '<p style="color: var(--td-text-color-disabled); text-align: center; padding: 20px;">文档内容为空</p>';
    return;
  }

  markdownHtml.value = renderDocumentPreviewMarkdown(text, undefined, {
    // 预览工具栏已经显示文件名；正文第一行的同名 H1 属于重复标题。
    titles: [props.fileName],
  });
}

function onImageLoad(e: Event) {
  const img = e.target as HTMLImageElement;
  imageNaturalWidth.value = img.naturalWidth;
  imageNaturalHeight.value = img.naturalHeight;
}

function getPreviewSourceKey(): string {
  if (props.sourceBlob) {
    return `resource-blob:${props.fileName}:${props.fileType}:${props.sourceBlob.size}:${props.sourceBlob.type}`;
  }
  if (props.knowledgeId) return `knowledge:${props.knowledgeId}`;
  if (props.sessionId && props.attachmentId) return `attachment:${props.sessionId}:${props.attachmentId}`;
  if (
    props.sessionId &&
    props.messageId &&
    Number.isInteger(props.artifactIndex) &&
    (props.artifactIndex as number) >= 0
  ) {
    return `artifact:${props.sessionId}:${props.messageId}:${props.artifactIndex}`;
  }
  return '';
}

// Skill-generated HTML is often a self-contained chart that needs its own
// scripts. Knowledge-base files and chat attachments are untrusted uploads:
// they stay as source, matching the previous text preview.
function allowsHtmlScriptPreview(): boolean {
  return getPreviewSourceKey().startsWith('artifact:');
}

async function fetchPreviewBlob(): Promise<Blob> {
  if (props.sourceBlob) return props.sourceBlob;
  if (props.knowledgeId) {
    return previewKnowledgeFile(props.knowledgeId);
  }
  if (props.sessionId && props.attachmentId) {
    return previewTemporaryAttachment(props.sessionId, props.attachmentId);
  }
  if (
    props.sessionId &&
    props.messageId &&
    Number.isInteger(props.artifactIndex) &&
    (props.artifactIndex as number) >= 0
  ) {
    return downloadArtifact(props.sessionId, props.messageId, props.artifactIndex as number);
  }
  throw new Error('Missing preview source');
}

async function renderMermaid(blob: Blob) {
  const text = await blob.text();
  const svg = await renderMermaidToSvg(text, `file-preview-mermaid-${Date.now()}`);
  if (svg) {
    mermaidSvg.value = sanitizeMarkdownHTML(svg);
    return;
  }
  previewType.value = 'text';
  await renderText(new Blob([text], { type: 'text/plain' }), 'mmd');
}

async function openMermaid() {
  const svg = mermaidSvg.value;
  if (!svg) return;
  // The diagram viewer mounts on body, outside the native fullscreen element.
  await exitPreviewFullscreen();
  if (props.active && previewRoot.value?.isConnected) openMermaidFullscreen(svg);
}

async function loadPreview() {
  const sourceKey = getPreviewSourceKey();
  if (!sourceKey || !props.active) return;
  // Unmount the previous spreadsheet (including its chart and worker) before
  // awaiting the new source's ACL. A retained Blob must not bridge sources.
  excelBlob.value = null;
  const version = ++previewLoadVersion;
  const knowledgeId = props.sourceBlob ? '' : props.knowledgeId || '';
  let ft = resolveFilePreviewExt(props.fileName, props.fileType);
  const knowledgePptx = !!knowledgeId && resolvePreviewKind(ft) === 'pptx';
  const knowledgeExcel = !!knowledgeId && resolvePreviewKind(ft) === 'excel';
  const scope = getPptxPreviewScope();
  const current = () => version === previewLoadVersion && props.active && getPreviewSourceKey() === sourceKey
    && getPptxPreviewScope() === scope;
  let cacheContext: { scope: string; sourceKey: string } | undefined;
  let authority = '';
  try {
    if (knowledgePptx || knowledgeExcel) {
      // Even a warm file must pass the actual current knowledge ACL on every
      // open. Hide retained content while this small metadata request runs.
      pptxPermissionChecking.value = true;
      clearPptxPageMedia();
      const response = await getKnowledgeDetails(knowledgeId);
      if (!current()) return;
      const metadata = response?.data;
      if (!metadata || metadata.id !== knowledgeId || metadata.deleted_at) throw new Error(t('preview.loadFailed'));
      pptxPermissionChecking.value = false;
      if (knowledgePptx) {
        const source = knowledgePptxSourceKey(knowledgeId, metadata);
        authority = JSON.stringify([scope, source]);
        if (scope && source) cacheContext = { scope, sourceKey: source };
        if (cacheContext && loadedForId === sourceKey && loadedPptxAuthority === authority && pptxData.value && pptxSourceSha256) {
          pptxMediaCacheContext = cacheContext;
          pptxPermissionChecking.value = false;
          await nextTick();
          if (!current()) return;
          watchPptxRender(sourceKey);
          void loadPptxPageMedia();
          return;
        }
      }
    } else if (loadedForId === sourceKey) {
      return;
    }
    if (!current()) return;
    cleanup();
    loading.value = true;
    error.value = '';
    htmlViewMode.value = allowsHtmlScriptPreview() ? 'render' : 'source';
    previewType.value = resolvePreviewKind(ft);
    const rawBlob = cacheContext
      ? await pptxPreviewCache.getOrLoad<Blob>(scope, `original:${cacheContext.sourceKey}`, async () => {
        const value = await previewKnowledgeFile(knowledgeId);
        return { value, bytes: value.size };
      })
      : await fetchPreviewBlob();
    if (!current()) return;
    let kind = resolvePreviewKind(ft);
    if (kind === 'unsupported') {
      const sample = new Uint8Array(await rawBlob.slice(0, FILE_PREVIEW_SNIFF_BYTES).arrayBuffer());
      const sniffed = sniffPreview(sample);
      kind = sniffed.kind;
      if (sniffed.ext) ft = sniffed.ext;
    }
    if (!current()) return;
    previewType.value = kind;
    if (kind === 'unsupported') return;
    const blob = ensureBlobType(rawBlob, ft);
    loadedForId = sourceKey;

    loading.value = false;
    await nextTick();

    previewExt = ft;
    switch (kind) {
      case 'pdf': {
        if (props.sourceMode) {
          pdfData.value = await blob.arrayBuffer();
        } else {
          blobUrl.value = URL.createObjectURL(blob);
        }
        break;
      }
      case 'image':
      case 'audio':
      case 'video': {
        blobUrl.value = URL.createObjectURL(blob);
        break;
      }
      case 'epub': {
        const rendered = await renderEpubPreview(await blob.arrayBuffer());
        epubObjectUrls = rendered.objectUrls;
        epubHtml.value = sanitizeHTML(rendered.html);
        break;
      }
      case 'html': {
        if (allowsHtmlScriptPreview()) {
          const previewHtml = buildHtmlPreview(await blob.text());
          blobUrl.value = URL.createObjectURL(new Blob([previewHtml], { type: 'text/html;charset=utf-8' }));
        }
        await renderText(blob, ft || 'html');
        break;
      }
      case 'docx': {
        await renderDocx(blob);
        break;
      }
      case 'excel': {
        excelBlob.value = blob;
        break;
      }
      case 'text': {
        await renderText(blob, ft);
        break;
      }
      case 'markdown': {
        await renderMarkdown(blob);
        // Markdown 里的 resource:// 等受保护图片需要带鉴权换 blob（无 override
        // 时走应用默认鉴权面：登录态 /files 代理）。已换过的元素幂等跳过。
        await nextTick();
        await hydrateProtectedFileImages(previewContent.value);
        break;
      }
      case 'pptx': {
        const prepare = async () => {
          const originalData = await blob.arrayBuffer();
          let sourceSha256 = '', sourceHashError = '';
          try { sourceSha256 = await sha256Hex(originalData); }
          catch { sourceHashError = 'source_fingerprint_unavailable'; }
          const prepared = await preparePptxPreview(originalData);
          const rendererData = knowledgeId ? await preparePptxOlePreview(prepared.data) : prepared.data;
          const value = { data: prepared.data, rendererData, sourceSha256, sourceHashError, slideCount: prepared.slideCount };
          return { value, bytes: prepared.data.byteLength + (rendererData === prepared.data ? 0 : rendererData.byteLength), cacheable: !sourceHashError };
        };
        const prepared = cacheContext
          ? await pptxPreviewCache.getOrLoad(cacheContext.scope, `prepared:${cacheContext.sourceKey}`, prepare)
          : (await prepare()).value;
        if (!current()) return;
        pptxSourceSha256 = prepared.sourceSha256;
        pptxSourceHashError = prepared.sourceHashError;
        pptxSlideCount = prepared.slideCount;
        pptxData.value = prepared.data;
        loadedPptxAuthority = authority;
        pptxMediaCacheContext = cacheContext;
        await setPptxRendererData(prepared.rendererData, sourceKey);
        if (!current()) return;
        void loadPptxPageMedia();
        break;
      }
      case 'mermaid': {
        await renderMermaid(blob);
        break;
      }
    }
    // PPTX location waits for the renderer event or the bounded slide-DOM fallback.
    if (kind !== 'pptx' && kind !== 'excel') scheduleLocate();
  } catch (err: any) {
    if (!current()) return;
    cleanup();
    error.value = err?.message || t('preview.loadFailed');
  } finally {
    if (version === previewLoadVersion) {
      loading.value = false;
      pptxPermissionChecking.value = false;
    }
  }
}

function cleanup() {
  loadedPptxAuthority = '';
  pptxMediaCacheContext = undefined;
  stopPptxRenderWatch();
  clearPptxPageMedia();
  pptxRendererCallbacks.value = null;
  pptxRenderReadySourceKey = '';
  pptxRenderFailureSourceKey = '';
  pptxRenderFailureReason = '';
  pptxRenderSignal = 'pending';
  if (blobUrl.value) {
    URL.revokeObjectURL(blobUrl.value);
    blobUrl.value = '';
  }
  textContent.value = '';
  highlightedCode.value = '';
  markdownHtml.value = '';
  excelBlob.value = null;
  mermaidSvg.value = '';
  htmlViewMode.value = 'render';
  pptxData.value = null;
  pptxRenderedData.value = null;
  pptxRendererKey.value = '';
  pptxSourceSha256 = '';
  pptxSourceHashError = '';
  pdfData.value = null;
  for (const url of epubObjectUrls) URL.revokeObjectURL(url);
  epubObjectUrls = [];
  epubHtml.value = '';
  clearLocateMarks();
  pptxSlideCount = 0;
  imageNaturalWidth.value = 0;
  imageNaturalHeight.value = 0;
  loadedForId = '';
  if (docxContainer.value) {
    docxContainer.value.innerHTML = '';
  }
}

// ── Citation locating ──
// Each preview kind reveals a SourceLocateRequest its own way: structural
// locators first (page, block, slide, rows, section, time), then the quoted
// text inside whatever they narrowed down, then the quotes anywhere.

type ImageMark = { left: number; top: number; width: number; height: number };
const imageMarks = ref<ImageMark[]>([]);
const audioQuote = ref('');
const audioRange = ref('');
let clearLocate: Array<() => void> = [];

function clearLocateMarks() {
  for (const clear of clearLocate) clear();
  clearLocate = [];
  imageMarks.value = [];
  audioQuote.value = '';
  audioRange.value = '';
}

let locateScheduled = false;
function scheduleLocate() {
  if (!props.locate || locateScheduled) return;
  const sourceKey = loadedForId;
  const token = props.locate.token;
  locateScheduled = true;
  void nextTick(() => {
    locateScheduled = false;
    if (!props.active || sourceKey !== loadedForId || token !== props.locate?.token) {
      if (props.active && loadedForId && props.locate) scheduleLocate();
      return;
    }
    applyLocate();
  });
}

watch(
  () => [props.locate?.token, props.sourceMode],
  () => {
    if (loadedForId && !loading.value) {
      if (previewType.value === 'pptx') {
        watchPptxRender(loadedForId);
        void loadPptxPageMedia();
        return;
      }
      scheduleLocate();
    }
  },
);

const NOT_FOUND: SourceLocateResult = { found: false, precise: false };

function findQuoteRange(root: Node | null | undefined, quotes: string[]): Range | null {
  if (!root) return null;
  const index = buildTextIndex(root);
  for (const quote of quotes) {
    const range = findTextRange(root, quote, index);
    if (range) return range;
  }
  return null;
}

function reveal(container: Element | null | undefined, target: Range | Element | null | undefined) {
  if (!container || !target) return;
  scrollRectIntoContainer(container, target.getBoundingClientRect());
}

function commitMarks(ranges: Range[], elements: Element[]) {
  if (ranges.length) clearLocate.push(highlightRanges(ranges));
  if (elements.length) clearLocate.push(highlightElements(elements));
}

/** Reveal the first quote found under `root`, scrolling `container`. */
function locateQuote(root: Element | null | undefined, container: Element | null | undefined, quotes: string[]): SourceLocateResult {
  const range = findQuoteRange(root, quotes);
  if (!range) return NOT_FOUND;
  commitMarks([range], []);
  reveal(container, range);
  return { found: true, precise: true };
}

/** Only exact evidence may narrow a structural source region. */
function evidenceQuotes(request: SourceLocateRequest): string[] {
  return request.scope ? [request.scope] : request.quotes;
}

function locateDocx(request: SourceLocateRequest): SourceLocateResult {
  const root = docxContainer.value;
  if (!root) return NOT_FOUND;
  const blocks = Array.from(root.querySelectorAll('section > article')).flatMap((article) =>
    Array.from(article.children).filter((c) => c.tagName === 'P' || c.tagName === 'TABLE'),
  );
  if (request.imageDigest) {
    const images = [...root.querySelectorAll('img')].filter(image => image.dataset.sourceImageDigest === request.imageDigest);
    if (images.length !== 1) return { ...NOT_FOUND, ...(images.length > 1 ? { reason: 'ambiguous' } : {}) };
    commitMarks([], images); reveal(root, images[0]);
    return { found: true, precise: false, granularity: 'block' };
  }
  if (request.imageContext) {
    const { before: beforeQuote, after: afterQuote } = request.imageContext;
    const before = beforeQuote ? findQuoteRange(root, [beforeQuote]) : null;
    const after = afterQuote ? findQuoteRange(root, [afterQuote]) : null;
    if ((beforeQuote && !before) || (afterQuote && !after) || (!before && !after)) return NOT_FOUND;
    const from = before?.cloneRange() || document.createRange();
    const to = after?.cloneRange() || document.createRange();
    if (before) from.collapse(false);
    else { from.selectNodeContents(root); from.collapse(true); }
    if (after) to.collapse(true);
    else { to.selectNodeContents(root); to.collapse(false); }
    // A chunk can end immediately after its image. In that case bound the
    // search by the next nonempty original paragraph, not the document end.
    for (const block of blocks) {
      if (!block.textContent?.trim()) continue;
      const range = document.createRange(); range.selectNodeContents(block);
      if (!after && range.compareBoundaryPoints(Range.START_TO_START, from) >= 0) { to.setStartBefore(block); to.collapse(true); break; }
      if (!before && range.compareBoundaryPoints(Range.END_TO_END, to) <= 0) { from.setStartAfter(block); from.collapse(true); }
    }
    const images = [...root.querySelectorAll('img')].filter(image => {
      const range = document.createRange(); range.selectNode(image);
      return range.compareBoundaryPoints(Range.START_TO_START, from) >= 0 &&
        range.compareBoundaryPoints(Range.END_TO_END, to) <= 0;
    });
    if (images.length !== 1) return NOT_FOUND;
    commitMarks([], images); reveal(root, images[0]);
    return { found: true, precise: false, granularity: 'block' };
  }
  const excerpt = request.quotes.length === 1 ? request.quotes[0] : '';
  if (excerpt && request.scope && excerpt !== request.scope && findInText(request.scope, excerpt)) {
    const range = findQuoteRange(root, [excerpt]);
    if (range) { commitMarks([range], []); reveal(root, range); return { found: true, precise: true }; }
  }
  const ranges: Range[] = [];
  let missed = false, partial = false, narrowed = false;
  for (const loc of request.locators.filter((l) => l.type === 'docx' && l.mapping === 'exact' && l.block)) {
    const el = blocks[(loc.block as number) - 1];
    const quote = sourceQuoteText(loc.quote || '');
    const sentence = request.sentence && findInText(quote, request.sentence) ? request.sentence : '';
    // Pagination may split Word paragraphs. Revalidate the complete quote
    // globally before accepting a shifted block, and reject duplicate matches.
    const region = quote ? findQuoteRange(el, [quote]) || findQuoteRange(root, [quote]) : null;
    if (!region) { missed = true; continue; }
    const range = sentence ? findQuoteRange(region.commonAncestorContainer.nodeType === Node.TEXT_NODE ? region.commonAncestorContainer.parentElement : region.commonAncestorContainer, [sentence]) : region;
    ranges.push(range || region);
    narrowed ||= !!sentence && !!range;
    partial ||= !!loc.partial;
  }
  if (!ranges.length) return locateQuote(root, root, evidenceQuotes(request));
  const complete = !missed && (!partial || narrowed || !!findQuoteRange(root, evidenceQuotes(request)));
  commitMarks(ranges, []);
  reveal(root, ranges[0]);
  return { found: true, precise: complete, ...(!complete ? { reason: 'partial' } : {}) };
}

function locatePptx(request: SourceLocateRequest): SourceLocateResult {
  const box = previewContent.value;
  if (!box) return NOT_FOUND;
  const slides = Array.from(box.querySelectorAll('.pptx-preview-slide-wrapper'));
  const locators = request.locators.filter((locator) => resolvePptxSlideLocator(locator, slides.length, false));
  if (!locators.length) return locateQuote(box, box, evidenceQuotes(request));
  const ranges: Range[] = [], regions: Element[] = [];
  let partial = false;
  let firstPage = 0;
  for (const loc of locators) {
    const page = resolvePptxSlideLocator(loc, slides.length, false)?.page;
    if (!page) continue;
    const slide = slides[page - 1];
    if (!slide) return NOT_FOUND;
    const slideRect = slide.getBoundingClientRect();
    if (!slide.isConnected || slideRect.width <= 0 || slideRect.height <= 0) {
      return { ...NOT_FOUND, reason: 'slide_layout_unavailable' };
    }
    firstPage ||= page;
    const own = slide.querySelector('.slide-wrapper') || slide;
    const range = loc.quote ? findQuoteRange(own, [loc.quote]) : null;
    const resolved = resolvePptxSlideLocator(loc, slides.length, !!range);
    partial ||= !!resolved?.partial;
    if (range && resolved?.precise) ranges.push(range); else regions.push(slide);
  }
  commitMarks(ranges, regions);
  const target = ranges[0] || regions[0];
  reveal(box, target);
  return {
    found: !!target,
    precise: !regions.length && !!ranges.length,
    granularity: regions.length ? 'block' : 'text',
    ...(partial ? { partial: true } : {}),
    ...(partial && !regions.length ? { reason: 'partial' } : {}),
    ...(firstPage ? { page: firstPage } : {}),
  };
}

function locateEpub(request: SourceLocateRequest): SourceLocateResult {
  const box = previewContent.value;
  if (!box) return NOT_FOUND;
  const locators = request.locators.filter((l) => l.type === 'section' && l.mapping === 'exact' && !l.partial && l.section);
  if (!locators.length) return locateQuote(box, box, evidenceQuotes(request));
  const ranges: Range[] = [], regions: Element[] = [];
  for (const loc of locators) {
    const section = box.querySelector(`[data-epub-section="${loc.section}"]`);
    if (!section) return NOT_FOUND;
    const range = loc.quote ? findQuoteRange(section, [loc.quote]) : null;
    if (range) ranges.push(range); else regions.push(section);
  }
  commitMarks(ranges, regions);
  reveal(box, ranges[0] || regions[0]);
  return { found: true, precise: !regions.length, granularity: regions.length ? 'block' : 'text' };
}

function locateText(request: SourceLocateRequest): SourceLocateResult {
  const box = previewContent.value;
  const code = box?.querySelector('code') || box;
  if (!box || !code) return NOT_FOUND;
  // Validate stored offsets against their full quote. A manual edit or a
  // pretty-printed JSON document must not reuse offsets from another version.
  const locators = request.locators.filter((l) => l.type === 'text' && !l.partial && (l.end || 0) > (l.start || 0));
  if (locators.length && previewType.value === 'text' && !shouldPrettyPrintJson(previewExt)) {
    const ranges = locators.map((l) => rangeForOffsets(code, l.start || 0, l.end || 0));
    if (ranges.every((r, i) => r && locators[i].quote && findInText(r.toString(), locators[i].quote!))) {
      commitMarks(ranges as Range[], []);
      reveal(box, ranges[0]);
      return { found: true, precise: true };
    }
  }
  return locateQuote(code, box, evidenceQuotes(request));
}

function locateMarkdown(request: SourceLocateRequest): SourceLocateResult {
  const box = previewContent.value;
  if (!box) return NOT_FOUND;
  const markdown = request.sourceMarkdown ?? request.scope ?? request.quotes[0] ?? '';
  const match = findMarkdownSourceRange(box, markdown, request.sentence);
  if (!match) return NOT_FOUND;
  commitMarks(match.ranges || [match.range], []);
  reveal(box, match.range);
  return { found: true, precise: match.exact, ...(!match.exact ? { granularity: 'block' } : {}) };
}

function locateImage(request: SourceLocateRequest): SourceLocateResult {
  const boxes = request.locators
    .filter((l) => l.type === 'pdf' && l.mapping === 'exact' && !l.partial && (l.page || 1) === 1 && l.bbox?.length === 4)
    .map((l) => {
      const [x0, y0, x1, y1] = l.bbox as number[];
      return { left: x0, top: y0, width: x1 - x0, height: y1 - y0 };
    });
  imageMarks.value = boxes;
  // Without regions the whole image is the cited source.
  return { found: true, precise: false, granularity: 'block' };
}

function formatClock(ms: number): string {
  const total = Math.max(0, Math.floor(ms / 1000));
  const m = Math.floor(total / 60);
  const s = total % 60;
  return `${String(m).padStart(2, '0')}:${String(s).padStart(2, '0')}`;
}

function locateAudio(request: SourceLocateRequest): SourceLocateResult {
  const audio = previewContent.value as HTMLAudioElement | null;
  const loc = request.locators.find((l) => l.type === 'time' && (l.end_ms || 0) > (l.start_ms || 0));
  if (!audio || !loc) return NOT_FOUND;
  // Seek only; playback stays the reader's choice.
  const seek = () => {
    audio.currentTime = (loc.start_ms || 0) / 1000;
  };
  if (audio.readyState >= 1) {
    seek();
  } else {
    // A newer locate clears this, so an older citation cannot win the seek.
    audio.addEventListener('loadedmetadata', seek, { once: true });
    clearLocate.push(() => audio.removeEventListener('loadedmetadata', seek));
  }
  audioRange.value = `${formatClock(loc.start_ms || 0)} – ${formatClock(loc.end_ms || 0)}`;
  audioQuote.value = loc.quote || '';
  return { found: true, precise: true };
}

function applyLocate() {
  const request = props.locate;
  clearLocateMarks();
  // The pdf.js viewer locates on its own and reports through @located.
  if (!request || previewType.value === 'pdf' || previewType.value === 'excel') return;
  if (request.unavailable) { emit('located', { ...NOT_FOUND, reason: 'unavailable' }); return; }
  let result = NOT_FOUND;
  try {
    switch (previewType.value) {
      case 'docx': result = locateDocx(request); break;
      case 'pptx': result = locatePptx(request); break;
      case 'epub': result = locateEpub(request); break;
      case 'text':
      case 'html': result = locateText(request); break;
      case 'markdown': result = locateMarkdown(request); break;
      case 'image': result = locateImage(request); break;
      case 'audio': result = locateAudio(request); break;
    }
  } catch (err) {
    console.warn('Locating the citation failed:', err);
  }
  emit('located', result);
}

watch(
  () => [props.active, props.knowledgeId, props.sessionId, props.attachmentId, props.messageId, props.artifactIndex, props.sourceBlob, props.fileName, props.fileType, previewAuth.token, previewAuth.selectedTenantId],
  ([active]) => {
    const scope = getPptxPreviewScope();
    if (scope !== previewSecurityScope) {
      previewSecurityScope = scope;
      // Rotate the shared cache's access generation even for a hidden viewer.
      // read validates scope without canceling another viewer's new-scope task.
      pptxPreviewCache.read(scope, '');
      if (previewType.value === 'pptx' || previewType.value === 'excel') {
        previewLoadVersion++;
        cleanup();
        loading.value = false;
        error.value = '';
      }
    }
    if (!active && previewType.value === 'excel') {
      previewLoadVersion++;
      cleanup();
      loading.value = false;
    }
    if (active && getPreviewSourceKey()) {
      loadPreview();
    }
  },
  { immediate: true }
);

onUnmounted(() => {
  previewLoadVersion++;
  stopPptxRenderWatch();
  cleanup();
});
</script>

<template>
  <div ref="previewRoot" tabindex="-1" :aria-label="fileName" class="document-preview" :class="{ 'is-fullscreen': isFullscreen, 'fill-height': fillHeight, 'is-permission-checking': pptxPermissionChecking }">
    <!-- Toolbar -->
    <Teleport :to="toolbarTarget || 'body'" :disabled="isFullscreen || !toolbarTarget">
      <div class="preview-toolbar" :class="{ 'is-inline': toolbarTarget && !isFullscreen }" v-if="isFullscreen || (!loading && !error && previewType !== 'unsupported')">
        <span v-if="isFullscreen" class="preview-toolbar-title" :title="fileName">{{ fileName }}</span>
        <div class="preview-toolbar-actions">
          <t-button
            v-if="previewType === 'html' && allowsHtmlScriptPreview()"
            theme="default" variant="text" size="small" shape="square"
            :title="htmlViewMode === 'render' ? $t('preview.htmlSource') : $t('preview.htmlRendered')"
            :aria-label="htmlViewMode === 'render' ? $t('preview.htmlSource') : $t('preview.htmlRendered')"
            @click="htmlViewMode = htmlViewMode === 'render' ? 'source' : 'render'"
          >
            <template #icon><t-icon :name="htmlViewMode === 'render' ? 'code' : 'browse'" /></template>
          </t-button>
          <t-button theme="default" variant="text" size="small" shape="square" :aria-pressed="isFullscreen"
            :title="isFullscreen ? $t('preview.exitFullscreen') : $t('preview.fullscreen')"
            :aria-label="isFullscreen ? $t('preview.exitFullscreen') : $t('preview.fullscreen')"
            @click="toggleFullscreen">
            <template #icon><t-icon :name="isFullscreen ? 'fullscreen-exit' : 'fullscreen'" /></template>
          </t-button>
        </div>
      </div>
    </Teleport>

    <div v-if="pptxPermissionChecking" class="preview-permission-loading" aria-live="polite">
      <t-loading size="small" /><span>{{ $t('preview.loading') }}</span>
    </div>
    <!-- Loading -->
    <div v-if="loading" class="preview-loading">
      <t-loading size="medium" />
      <span class="loading-text">{{ $t('preview.loading') }}</span>
    </div>

    <!-- Error -->
    <div v-else-if="error" class="preview-error">
      <t-icon name="error-circle" size="48px" />
      <p>{{ error }}</p>
      <t-button theme="primary" size="small" @click="loadedForId = ''; loadPreview()">
        {{ $t('preview.retry') }}
      </t-button>
    </div>

    <!-- Unsupported -->
    <div v-else-if="previewType === 'unsupported'" class="preview-unsupported">
      <t-icon name="file-unknown" size="48px" />
      <p>{{ $t('preview.unsupported') }}</p>
      <p class="unsupported-hint">{{ $t('preview.unsupportedHint') }}</p>
    </div>

    <!-- PDF -->
    <div v-else-if="previewType === 'pdf' && pdfData" class="preview-pdf preview-pdf--source">
      <PdfSourceViewer :data="pdfData" :file-name="fileName" :locate="locate" @located="(r) => emit('located', r)" />
    </div>
    <div v-else-if="previewType === 'pdf' && blobUrl" class="preview-pdf">
      <iframe ref="previewContent" tabindex="0" :title="fileName" :src="blobUrl" class="pdf-iframe" @load="onPreviewFrameLoad" />
    </div>

    <!-- HTML: artifacts render in a unique-origin iframe; other sources stay as source. -->
    <div v-else-if="previewType === 'html'" class="preview-html">
      <iframe
        v-if="allowsHtmlScriptPreview() && htmlViewMode === 'render' && blobUrl"
        ref="previewContent"
        tabindex="0"
        :title="fileName"
        :src="blobUrl"
        class="html-iframe"
        sandbox="allow-scripts"
        referrerpolicy="no-referrer"
        @load="onPreviewFrameLoad"
      />
      <pre v-else-if="!allowsHtmlScriptPreview() || htmlViewMode === 'source'" ref="previewContent" tabindex="0" :aria-label="fileName" class="code-preview"><code class="hljs" v-html="highlightedCode"></code></pre>
    </div>

    <!-- Image -->
    <div v-else-if="previewType === 'image' && blobUrl" ref="previewContent" tabindex="0" :aria-label="fileName" class="preview-image">
      <div class="image-wrapper">
        <div class="image-frame">
          <img :src="blobUrl" :alt="fileName" @load="onImageLoad" />
          <div
            v-for="(mark, i) in imageMarks"
            :key="i"
            class="image-locate-mark"
            aria-hidden="true"
            :style="{ left: `${mark.left * 100}%`, top: `${mark.top * 100}%`, width: `${mark.width * 100}%`, height: `${mark.height * 100}%` }"
          />
        </div>
        <div v-if="imageNaturalWidth" class="image-info">
          {{ imageNaturalWidth }} × {{ imageNaturalHeight }} px
        </div>
      </div>
    </div>

    <!-- DOCX -->
    <div v-else-if="previewType === 'docx'" class="preview-docx">
      <div ref="docxContainer" tabindex="0" :aria-label="fileName" class="docx-container" />
    </div>

    <!-- PPTX -->
    <div v-else-if="previewType === 'pptx' && pptxData" ref="previewContent" tabindex="0" :aria-label="fileName" class="preview-pptx">
      <details
        v-if="sourceMode && exactPptxLocatePage() && pptxPageMediaState !== 'idle' && pptxPageMediaState !== 'none'"
        class="pptx-source-media"
        :open="pptxInlineMediaFailed || pptxPageMediaState === 'failed' || pptxPageMediaState === 'partial'"
        aria-live="polite"
        :aria-label="`第${exactPptxLocatePage()}页引用图像`"
      >
        <summary class="pptx-source-media__heading">
          <strong>第{{ exactPptxLocatePage() }}页引用图像</strong>
          <span v-if="pptxSourceHashError">无法校验来源文件，暂不能显示原件图像。</span>
          <span v-else-if="pptxPageMediaState === 'discovering'">正在读取此页的嵌入图像…</span>
          <span v-else-if="pptxPageMediaState === 'loading'">正在载入原件图像…</span>
          <span v-else-if="pptxPageMediaState === 'loaded'">已载入此页的原件图像</span>
          <span v-else-if="pptxPageMediaState === 'partial'">部分原件图像载入失败</span>
          <span v-else-if="pptxPageMediaState === 'failed'">原件图像读取失败</span>
          <button
            v-if="!pptxSourceHashError && (pptxPageMediaState === 'failed' || pptxPageMediaState === 'partial')"
            type="button"
            class="pptx-source-media__retry"
            @click="retryPptxPageMedia"
          >重试</button>
        </summary>
        <p class="pptx-source-media__note">以下为该页从原文件读取的独立图像，不代表整页内容均已完整渲染。</p>
        <div v-if="pptxPageMedia.length" class="pptx-source-media__items">
          <figure v-for="media in pptxPageMedia" :key="`${media.generation}:${media.part}`" class="pptx-source-media__item">
            <figcaption>
              <span>第{{ exactPptxLocatePage() }}页 · 图{{ media.shapeIndexes.join('、') }}</span>
              <details>
                <summary>来源信息</summary>
                <code>{{ fileName }} · {{ media.part }}</code>
              </details>
            </figcaption>
            <img
              v-if="media.objectUrl"
              :src="media.objectUrl"
              :alt="`第${exactPptxLocatePage()}页图${media.shapeIndexes.join('、')}原件图像`"
              @load="onPptxMediaImageLoad(media.part, media.generation)"
              @error="onPptxMediaImageError(media.part, media.generation)"
            />
            <span v-else-if="media.state === 'failed'" class="pptx-source-media__error">此图像读取失败，可重试或打开原文件。</span>
            <span v-else class="pptx-source-media__pending">正在读取…</span>
          </figure>
        </div>
      </details>
      <vue-office-pptx :key="pptxRendererKey" :src="pptxRenderedData" @rendered="pptxRendererCallbacks?.onRendered" @error="pptxRendererCallbacks?.onError" />
    </div>

    <!-- Excel -->
    <div v-else-if="previewType === 'excel' && excelBlob" class="preview-excel">
      <ExcelSourceViewer :blob="excelBlob" :extension="previewExt" :file-name="fileName" :locate="locate" @located="emit('located', $event)" />
    </div>

    <!-- EPUB -->
    <div v-else-if="previewType === 'epub' && epubHtml" ref="previewContent" tabindex="0" :aria-label="fileName" class="preview-markdown preview-epub">
      <div class="markdown-body" v-html="epubHtml" />
    </div>

    <!-- Markdown -->
    <div v-else-if="previewType === 'markdown' && markdownHtml" ref="previewContent" tabindex="0" :aria-label="fileName" class="preview-markdown">
      <div class="markdown-body" v-html="markdownHtml" />
    </div>

    <!-- Text / Code -->
    <div v-else-if="previewType === 'text' && highlightedCode" class="preview-text">
      <pre ref="previewContent" tabindex="0" :aria-label="fileName" class="code-preview"><code class="hljs" v-html="highlightedCode"></code></pre>
    </div>

    <!-- Mermaid -->
    <div v-else-if="previewType === 'mermaid' && mermaidSvg" ref="previewContent" tabindex="0" :aria-label="fileName" class="preview-mermaid" @click="openMermaid">
      <div class="mermaid-body" v-html="mermaidSvg" />
    </div>

    <!-- Audio -->
    <div v-else-if="previewType === 'audio' && blobUrl" class="preview-audio">
      <div class="audio-wrapper">
        <t-icon name="sound" size="48px" />
        <p class="audio-filename">{{ fileName }}</p>
        <audio ref="previewContent" controls :src="blobUrl" class="audio-element">
          {{ $t('preview.audioNotSupported') }}
        </audio>
        <div v-if="audioRange" class="audio-locate">
          <span class="audio-locate__time">{{ audioRange }}</span>
          <p v-if="audioQuote" class="audio-locate__quote">{{ audioQuote }}</p>
        </div>
      </div>
    </div>

    <!-- Video -->
    <div v-else-if="previewType === 'video' && blobUrl" class="preview-video">
      <video ref="previewContent" controls playsinline :src="blobUrl" class="video-element">
        {{ $t('preview.videoNotSupported') }}
      </video>
    </div>
  </div>
</template>

<style scoped lang="less">
// ── Design tokens ──
@border-color: var(--td-component-stroke);
@border-radius: var(--app-radius-sm);
@bg-white: var(--td-bg-color-container);
@bg-subtle: var(--td-bg-color-container);
@bg-muted: var(--td-bg-color-secondarycontainer);
@text-primary: var(--td-text-color-primary);
@text-secondary: var(--td-text-color-secondary);
@text-tertiary: var(--td-text-color-placeholder);
@text-disabled: var(--td-text-color-disabled);
@accent: var(--td-brand-color);
@accent-hover: var(--td-brand-color-active);
@accent-bg: var(--td-success-color-light);
@accent-bg-hover: var(--td-success-color-light);
@error-color: var(--td-error-color);
@table-border: var(--td-component-stroke);
@preview-max-h: calc(100vh - 200px);
// Note: <html> carries a `zoom` multiplier for font-size control, so 100vh
// is evaluated against the unscaled viewport and the resulting max-height
// may exceed the real viewport by the zoom factor (≤12.5% at "large").
// That produces an extra bit of scroll inside the non-fullscreen preview,
// which is acceptable for document reading. Not worth the complexity of
// inverse-scaling here.
@transition: all var(--app-motion-base) ease;

// ── Shared container mixin ──
.preview-container() {
  border: 1px solid @border-color;
  border-radius: @border-radius;
  overflow: auto;
  max-height: @preview-max-h;
  background: @bg-white;
}

.document-preview {
  min-height: 200px;
  position: relative;

  &.fill-height,
  &.is-fullscreen {
    height: 100%;
    min-height: 0;
    display: flex;
    flex-direction: column;

    .preview-pdf,
    .preview-pptx,
    .preview-docx,
    .preview-image,
    .preview-excel,
    .preview-markdown,
    .preview-text,
    .preview-html,
    .preview-mermaid,
    .preview-audio,
    .preview-video,
    .preview-loading,
    .preview-error,
    .preview-unsupported {
      flex: 1;
      min-height: 0;
      max-height: none;
    }

    .preview-pdf {
      height: auto;
      min-height: 0;
    }

    .preview-pptx {
      overflow: auto;
    }

    .preview-docx,
    .preview-excel,
    .preview-text {
      display: flex;
      flex-direction: column;

      .docx-container,
      .excel-container,
      .code-preview {
        flex: 1;
        min-height: 0;
        max-height: none;
        height: auto;
      }
    }

    .preview-image .image-wrapper img {
      max-height: 100%;
    }

    .preview-excel .excel-container,
    .preview-markdown,
    .preview-text .code-preview,
    .preview-html .code-preview {
      max-height: none;
    }

    .preview-html .html-iframe {
      height: 100%;
    }

    .preview-html {
      height: auto;
      min-height: 0;
    }
  }
}

.is-fullscreen {
  position: fixed;
  top: 0;
  left: 0;
  right: 0;
  bottom: 0;
  z-index: 2001;
  background: var(--td-bg-color-container);
  padding: 0;
  overflow: hidden;

  .preview-toolbar {
    padding: 12px 16px;
    margin-bottom: 0;
  }

  .preview-pptx {
    height: auto;
    min-height: 0;
    overflow: auto;
    border: none;

    :deep(.pptx-preview-wrapper) {
      height: auto !important;
      overflow-y: visible !important;
    }
  }

  .preview-image {
    display: flex;
    justify-content: center;
    align-items: center;
  }
}

.preview-toolbar {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-shrink: 0;
  min-width: 0;
  padding: 8px 0;
  margin-bottom: 8px;
  border-bottom: 1px solid @border-color;
  background: @bg-white;
}

.preview-toolbar-title {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: @text-primary;
  font-size: var(--app-text-base);
  font-weight: 500;
}

.preview-toolbar.is-inline {
  padding: 0;
  margin: 0;
  border: 0;
  background: transparent;
}

.preview-toolbar-actions {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  justify-content: flex-end;
  gap: 8px;
  margin-left: auto;

  :deep(.t-button) {
    flex-shrink: 0;
    width: 30px;
    height: 30px;
    border-radius: 7px;
    color: @text-secondary;
  }

  :deep(.t-button__icon) {
    margin: 0;
    font-size: var(--app-text-xl);
  }
}

// ── States ──
.preview-loading {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  padding: 60px 20px;
  gap: 16px;
  .loading-text { color: @text-tertiary; font-size: var(--app-text-base); }
}

.preview-error {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  padding: 60px 20px;
  gap: 12px;
  color: @error-color;
  p { margin: 0; font-size: var(--app-text-base); color: @text-secondary; }
}

.preview-unsupported {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  padding: 60px 20px;
  gap: 12px;
  color: @text-disabled;
  p { margin: 0; font-size: var(--app-text-base); color: @text-secondary; }
  .unsupported-hint { font-size: var(--app-text-sm); color: @text-tertiary; }
}

// ── PDF ──
.preview-pdf {
  width: 100%;
  height: @preview-max-h;
  min-height: 500px;
  .pdf-iframe {
    width: 100%;
    height: 100%;
    border: none;
    border-radius: @border-radius;
  }
}

// ── HTML ──
.preview-html {
  width: 100%;
  height: @preview-max-h;
  min-height: 420px;
  display: flex;
  flex-direction: column;
  .html-iframe {
    flex: 1;
    width: 100%;
    min-height: 0;
    border: 1px solid @border-color;
    border-radius: @border-radius;
    background: @bg-white;
  }
  .code-preview {
    .preview-container();
    flex: 1;
    min-height: 0;
    margin: 0;
    padding: 16px;
    background: @bg-subtle;
    font-size: var(--app-text-md);
    line-height: 1.6;
    code {
      white-space: pre;
      word-wrap: normal;
      display: block;
      background: transparent;
    }
  }
}

// ── Mermaid ──
.preview-mermaid {
  .preview-container();
  display: flex;
  justify-content: center;
  padding: 24px 16px;
  cursor: zoom-in;
  .mermaid-body {
    max-width: 100%;
    :deep(svg) {
      max-width: 100%;
      height: auto;
    }
  }
}

// ── PDF (pdf.js, source mode) ──
.preview-pdf--source {
  height: @preview-max-h;
  border: 1px solid @border-color;
  border-radius: @border-radius;
  overflow: hidden;
}

// ── Image ──
.image-frame {
  position: relative;
  display: inline-block;
  line-height: 0;
}

.image-locate-mark {
  position: absolute;
  pointer-events: none;
  border-radius: var(--app-radius-xs);
  background: var(--app-source-highlight-soft-bg);
  outline: 1px solid var(--app-source-highlight-border);
}

.preview-image {
  overflow: auto;
  display: flex;
  justify-content: center;
  padding: 20px 0;
  .image-wrapper {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 8px;
    img {
      max-width: 100%;
      max-height: calc(100vh - 280px);
      border-radius: @border-radius;
      box-shadow: 0 2px 12px color-mix(in srgb, var(--td-brand-color) 8%, transparent);
      object-fit: contain;
    }
    .image-info { font-size: var(--app-text-sm); color: @text-tertiary; }
  }
}

// ── Markdown ──
.preview-markdown {
  .preview-container();
  padding: 20px 24px;
}

// ── DOCX ──
.preview-docx {
  .docx-container { .preview-container(); }
}

// ── PPTX ──
.preview-pptx {
  max-height: @preview-max-h;
  min-height: 500px;
  border: 1px solid @border-color;
  border-radius: @border-radius;
  overflow: auto;
  background: @bg-subtle;

  .pptx-source-media {
    position: sticky;
    top: 0;
    z-index: 3;
    max-height: 34vh;
    overflow: auto;
    padding: 10px 14px;
    border-bottom: 1px solid @border-color;
    background: @bg-subtle;
  }

  .pptx-source-media__heading {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 8px 12px;
    color: @text-primary;
    font-size: var(--app-text-sm);
  }

  .pptx-source-media__heading > span,
  .pptx-source-media__note,
  .pptx-source-media__pending,
  .pptx-source-media__error {
    color: @text-secondary;
    font-size: var(--app-text-xs);
  }

  .pptx-source-media__retry {
    border: 0;
    padding: 2px 8px;
    border-radius: @border-radius;
    color: @text-primary;
    background: @accent-bg;
    cursor: pointer;
  }

  .pptx-source-media__note { margin: 4px 0 8px; }

  .pptx-source-media__items {
    display: flex;
    flex-wrap: wrap;
    gap: 10px;
  }

  .pptx-source-media__item {
    display: grid;
    justify-items: start;
    gap: 6px;
    min-width: 180px;
    max-width: min(100%, 520px);
    margin: 0;
    padding: 8px;
    border: 1px solid @border-color;
    border-radius: @border-radius;
    background: var(--td-bg-color-container);
  }

  .pptx-source-media__item figcaption {
    display: grid;
    gap: 3px;
    font-size: var(--app-text-xs);
    color: @text-secondary;
  }

  .pptx-source-media__item code {
    overflow-wrap: anywhere;
    color: @text-tertiary;
  }

  .pptx-source-media__item details summary {
    cursor: pointer;
  }

  .pptx-source-media__item img {
    display: block;
    max-width: 100%;
    max-height: 200px;
    object-fit: contain;
  }

  :deep(.pptx-preview-wrapper) {
    height: auto !important;
    overflow-y: visible !important;
  }
}

// ── Excel ──
.preview-excel {
  .preview-container();
  padding: 0;
}

// ── Text / Code ──
.preview-text {
  .code-preview {
    .preview-container();
    margin: 0;
    padding: 16px;
    background: @bg-subtle;
    font-size: var(--app-text-md);
    line-height: 1.6;
    code {
      white-space: pre;
      word-wrap: normal;
      display: block;
      background: transparent;
    }
  }
}

// ── Audio ──
.preview-audio {
  display: flex;
  justify-content: center;
  padding: 40px 20px;
  .audio-wrapper {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 16px;
    color: @text-secondary;
    .audio-filename { font-size: var(--app-text-base); color: @text-primary; margin: 0; }
    .audio-element { width: 100%; max-width: 480px; }
  }
  .audio-locate {
    width: 100%;
    max-width: 480px;
    padding: 10px 12px;
    border-radius: @border-radius;
    background: var(--app-source-highlight-soft-bg);
    color: @text-primary;
    &__time {
      font-size: var(--app-text-sm);
      font-variant-numeric: tabular-nums;
      color: @text-secondary;
    }
    &__quote {
      margin: 4px 0 0;
      font-size: var(--app-text-md);
      line-height: 1.6;
    }
  }
}

// ── Video ──
.preview-video {
  display: flex;
  justify-content: center;
  align-items: center;
  padding: 16px;
  min-height: 280px;
  .video-element {
    width: 100%;
    max-height: calc(100vh - 240px);
    border-radius: @border-radius;
    background: #000;
  }
}

// ── Deep styles (v-html / third-party components) ──

// Shared table mixin for v-html content
.preview-table(@header-bg: @accent-bg; @hover-bg: @accent-bg) {
  width: 100%;
  border-collapse: collapse;
  font-size: var(--app-text-md);
  th, td {
    border: 1px solid @table-border;
    padding: 6px 12px;
    text-align: left;
  }
  th {
    background: @header-bg;
    font-weight: 600;
    color: @text-primary;
  }
  tr:hover td {
    background: @hover-bg;
    transition: @transition;
  }
}

:deep(.markdown-body) {
  font-size: var(--app-text-base);
  line-height: 1.75;
  color: @text-primary;
  word-break: break-word;
  // 正文窄栏：全屏 / 宽抽屉下限制行宽，避免中文长行难以阅读。
  max-width: 680px;
  margin-inline: auto;

  h1, h2, h3, h4, h5, h6 {
    margin-top: 20px;
    margin-bottom: 10px;
    font-weight: 600;
    line-height: 1.4;
  }
  h1:first-child,
  h2:first-child,
  h3:first-child,
  h4:first-child,
  h5:first-child,
  h6:first-child { margin-top: 0; }
  // 标题层级：旧规则里 h4~h6 没有字号，会退回正文字号（14px），和 h3 的
  // 17px 几乎看不出差别；这里补齐 24 / 20 / 18 / 16 / 15 / 14 的完整层级。
  h1 { font-size: var(--app-text-4xl); border-bottom: 1px solid @border-color; padding-bottom: 8px; }
  h2 { font-size: var(--app-text-3xl); border-bottom: 1px solid @border-color; padding-bottom: 6px; }
  h3 { font-size: var(--app-text-2xl); }
  h4 { font-size: var(--app-text-xl); margin-top: 16px; margin-bottom: 8px; }
  h5 { font-size: var(--app-text-lg); margin-top: 14px; margin-bottom: 6px; }
  h6 { font-size: var(--app-text-base); margin-top: 12px; margin-bottom: 6px; }

  p { margin: 10px 0; }
  p:last-child { margin-bottom: 0; }
  blockquote {
    margin: 14px 0;
    padding: 10px 16px;
    border-left: 4px solid @border-color;
    background: @bg-subtle;
    color: var(--td-text-color-secondary);
  }
  blockquote > :first-child { margin-top: 0; }
  blockquote > :last-child { margin-bottom: 0; }
  ul, ol { padding-left: 28px; margin: 10px 0; }
  li { margin: 4px 0; line-height: 1.75; }
  li > ul, li > ol { margin: 4px 0; padding-left: 24px; }
  li > p { margin: 4px 0; }
  ul li:has(> .task-checkbox) { list-style: none; }
  .task-checkbox {
    display: inline-block;
    width: 14px;
    height: 14px;
    margin-right: 6px;
    border: 1px solid @border-color;
    border-radius: var(--app-radius-xs);
    vertical-align: -2px;
    position: relative;
    &.checked {
      background: @accent;
      border-color: @accent;
      &::after {
        content: '';
        position: absolute;
        left: 4px;
        top: 1px;
        width: 4px;
        height: 8px;
        border: solid #fff;
        border-width: 0 2px 2px 0;
        transform: rotate(45deg);
      }
    }
  }

  table {
    .preview-table(@bg-muted; var(--td-bg-color-container-hover));
    margin: 12px 0;
  }

  pre {
    margin: 12px 0;
    padding: 14px;
    background: @bg-subtle;
    border-radius: @border-radius;
    overflow: auto;
    font-size: var(--app-text-md);
    line-height: 1.5;
    code { background: transparent; padding: 0; }
  }
  code {
    background: var(--td-bg-color-secondarycontainer);
    padding: 1px 5px;
    border-radius: var(--app-radius-xs);
    font-size: 0.92em;
  }
  img { max-width: 100%; border-radius: var(--app-radius-xs); }
  hr { border: none; border-top: 1px solid @border-color; margin: 20px 0; }
  a { color: @accent; text-decoration: none; &:hover { color: @accent-hover; text-decoration: underline; } }
  strong { font-weight: 600; }
}

:deep(.docx-preview-wrapper) {
  padding: 20px;
  max-width: 100%;
  width: 100%;
  box-sizing: border-box;
  overflow-x: auto; // 如果内容过宽，允许水平滚动而不是溢出
  
  // 约束所有子元素的宽度
  * {
    max-width: 100%;
    box-sizing: border-box;
  }
  
  // 特别处理表格
  table {
    width: 100%;
    table-layout: auto;
    word-wrap: break-word;
  }
  
  // 处理图片
  img {
    max-width: 100%;
    height: auto;
  }
  
  // 处理可能的固定宽度元素
  [style*="width"] {
    max-width: 100% !important;
  }
}

:deep(.vue-office-pptx) {
  width: 100%;
  min-height: 100%;
}

:deep(.vue-office-pptx-main) {
  width: 100%;
  min-height: 100%;
}

:deep(.excel-sheet) {
  padding: 0;
  .excel-sheet-name {
    position: sticky;
    top: 0;
    background: @accent-bg;
    padding: 8px 16px;
    font-weight: 600;
    font-size: var(--app-text-md);
    color: @text-primary;
    border-bottom: 1px solid @border-color;
    z-index: 1;
  }
  table {
    .preview-table();
    th, td {
      white-space: nowrap;
      max-width: 300px;
      overflow: hidden;
      text-overflow: ellipsis;
    }
  }
}
</style>

<!-- highlight.js github.css is a light theme imported globally; its token
     colors (notably the base #24292e) become unreadable on the dark
     container background used in dark mode. Remap the palette to the
     github-dark colors when dark mode is active. Non-scoped on purpose so
     it covers every hljs block (txt/code preview, markdown code fences). -->
<style lang="less">
html[theme-mode="dark"] {
  .hljs {
    color: #c9d1d9;
    background: transparent;
  }
  .hljs-doctag,
  .hljs-keyword,
  .hljs-meta .hljs-keyword,
  .hljs-template-tag,
  .hljs-template-variable,
  .hljs-type,
  .hljs-variable.language_ {
    color: #ff7b72;
  }
  .hljs-title,
  .hljs-title.class_,
  .hljs-title.class_.inherited__,
  .hljs-title.function_ {
    color: #d2a8ff;
  }
  .hljs-attr,
  .hljs-attribute,
  .hljs-literal,
  .hljs-meta,
  .hljs-number,
  .hljs-operator,
  .hljs-variable,
  .hljs-selector-attr,
  .hljs-selector-class,
  .hljs-selector-id {
    color: #79c0ff;
  }
  .hljs-regexp,
  .hljs-string,
  .hljs-meta .hljs-string {
    color: #a5d6ff;
  }
  .hljs-built_in,
  .hljs-symbol {
    color: #ffa657;
  }
  .hljs-comment,
  .hljs-code,
  .hljs-formula {
    color: #8b949e;
  }
  .hljs-name,
  .hljs-quote,
  .hljs-selector-tag,
  .hljs-selector-pseudo {
    color: #7ee787;
  }
  .hljs-subst {
    color: #c9d1d9;
  }
  .hljs-section {
    color: #1f6feb;
    font-weight: bold;
  }
  .hljs-bullet {
    color: #f2cc60;
  }
  .hljs-emphasis {
    color: #c9d1d9;
    font-style: italic;
  }
  .hljs-strong {
    color: #c9d1d9;
    font-weight: bold;
  }
  .hljs-addition {
    color: #aff5b4;
    background-color: #033a16;
  }
  .hljs-deletion {
    color: #ffdcd7;
    background-color: #67060c;
  }
}
</style>

<style lang="less">
/* Citation highlights live outside the scoped block: they apply to DOM that
   third-party renderers (docx-preview, pptx-preview, SheetJS) create. */
::highlight(source-locate) {
  background-color: var(--app-source-highlight-bg);
}

mark.source-locate-mark {
  background-color: var(--app-source-highlight-bg);
  color: inherit;
  border-radius: var(--app-radius-xs);
  box-decoration-break: clone;
  -webkit-box-decoration-break: clone;
}

.source-locate-block {
  background-color: var(--app-source-highlight-soft-bg) !important;
  box-shadow: inset 3px 0 var(--app-source-highlight-border);
}

/* Slides are injected imperatively by vue-office and do not carry this
   component's scoped-style attribute. */
.preview-pptx :deep(.source-locate-block) {
  background-color: var(--app-source-highlight-soft-bg) !important;
  box-shadow: inset 3px 0 var(--app-source-highlight-border);
}

/* Cell backgrounds can obscure a row's marker. Paint each cell once and
   keep the accent on the leading edge, without boxing every table row. */
tr.source-locate-block {
  background-color: transparent !important;
  box-shadow: none;
}

tr.source-locate-block > td {
  background-color: var(--app-source-highlight-soft-bg) !important;
}

tr.source-locate-block > td:first-child {
  box-shadow: inset 3px 0 var(--app-source-highlight-border);
}

img.source-locate-block {
  outline: 1px solid var(--app-source-highlight-border);
  outline-offset: 2px;
}
</style>

<style scoped>
.preview-permission-loading { position: absolute; inset: 0; z-index: 2; display: flex; justify-content: center; align-items: center; gap: 8px; background: var(--td-bg-color-container); }
.is-permission-checking .preview-pptx { visibility: hidden; }
.preview-pptx :deep(img[data-pptx-media-state="pending"]), .preview-pptx :deep(img[data-pptx-media-state="loading"]) { background: var(--td-bg-color-secondarycontainer); }
</style>
