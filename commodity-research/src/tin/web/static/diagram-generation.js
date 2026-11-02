/* The generated draft uses the existing Markdown editor and import actions. */
let dgGeneratorImage = null, dgGeneratorEpoch = 0, dgGeneratorController = null;
let dgGeneratorJob = null, dgJobTimer, dgJobEpoch = 0, dgJobReadController = null;
let dgSubmittingJob = false, dgSubmission = null, dgGenerationContext = null, dgGenerationResult = null;
let dgLatestResultJob = null;
const dgJobId = value => typeof value === 'string' && /^[A-Za-z0-9_-]{16,128}$/.test(value) ? value : '';
const dgJobActive = () => dgGeneratorJob && ['queued','running'].includes(dgGeneratorJob.state);
const dgGeneratorOpen = () => {
  const drawer = document.getElementById('dg-md-drawer');
  return drawer && !drawer.classList.contains('hidden') && !drawer.classList.contains('translate-x-full');
};
function dgStopJobMonitor() {
  clearTimeout(dgJobTimer); dgJobEpoch++;
  dgJobReadController?.abort(); dgJobReadController = null;
}
function dgCancelGeneration() {
  dgGeneratorEpoch++;
  dgGeneratorController?.abort();dgGeneratorController = null;
  dgStopJobMonitor();
  const restore = document.getElementById('dg-ai-restore-result');
  if (restore && !dgGeneratorOpen()) restore.hidden = true;
  const button = document.getElementById('dg-ai-run');
  if (button) button.disabled = dgSubmittingJob || !!dgJobActive();
  const rebuild = document.getElementById('dg-ai-rebuild');
  if (rebuild) rebuild.disabled = dgSubmittingJob || !!dgJobActive();
  return dgGeneratorEpoch;
}
function dgGenerationIsCurrent(operation) { return operation === dgGeneratorEpoch; }
function dgHasBackgroundGeneration() { return !!dgGeneratorJob || dgSubmittingJob; }
function dgGenerationRequestBody() {
  return JSON.stringify({model_id:document.getElementById('dg-ai-model').value,
    instructions:document.getElementById('dg-ai-instructions').value,image:dgGeneratorImage,
    source_id:document.getElementById('dg-ai-source').value || null});
}
function dgAnnounceJob(job, templateId) {
  const workbenchId = window.RESEARCH_CONTEXT?.workbench;
  if (parent === window || !workbenchId || !dgJobId(job?.id)) return;
  parent.postMessage({type:templateId ? 'research-analysis-job-applied' : 'research-analysis-job-started',
    purpose:'diagram',workbenchId,jobId:job.id,...(templateId ? {templateId} : {})},location.origin);
}
function dgGenerationApplied(templateId) {
  if (dgGeneratorJob) dgAnnounceJob(dgGeneratorJob, templateId);
  dgStopJobMonitor(); dgGeneratorJob = null; dgGenerationContext = null; dgGenerationResult = null;
}
async function dgGeneratorRequest(path, options) {
  const response = await researchFetch((window.RESEARCH_PREFIX || '') + '/api/sn/diagram/generation' + path, options);
  const data = await response.json();
  if (!response.ok) throw new Error(typeof data.detail === 'string' ? data.detail : '生成未完成，请核对输入和模型配置');
  return data;
}
async function dgJobRequest(path, options = {}) {
  const response = await researchFetch((window.RESEARCH_PREFIX || '') + '/api/sn/diagram/jobs' + path, options);
  const data = await response.json();
  if (!response.ok) throw new Error(typeof data.detail === 'string' ? data.detail : '后台生成暂不可用，请稍后重试');
  return data;
}
async function dgLoadGenerator() {
  const own = dgGeneratorEpoch;
  const restore = document.getElementById('dg-ai-restore-result');
  if (restore) restore.hidden = true;
  dgGeneratorController = new AbortController();
  const signal = dgGeneratorController.signal;
  const panel = document.getElementById('dg-generator');
  if (!panel) return;
  const status = document.getElementById('dg-ai-status');
  try {
    const [models, sources] = await Promise.all([dgGeneratorRequest('/models',{signal}),dgGeneratorRequest('/sources',{signal})]);
    if (own !== dgGeneratorEpoch) return;
    const picker = document.getElementById('dg-ai-model'), selected = picker.value;
    picker.replaceChildren();
    models.models.forEach(model => {
      const option = new Option(model.name + (model.supports_vision ? ' · 视觉' : ''),model.id);
      option.dataset.vision = String(model.supports_vision === true);
      picker.add(option);
    });
    if ([...picker.options].some(option => option.value === selected)) picker.value = selected;
    const sourcePicker = document.getElementById('dg-ai-source'), selectedSource = sourcePicker.value;
    sourcePicker.replaceChildren(new Option('知识库原件',''));
    sources.sources.forEach(source => {
      const option = new Option(source.name,source.id);option.dataset.kind = source.kind || 'text';sourcePicker.add(option);
    });
    if ([...sourcePicker.options].some(option => option.value === selectedSource)) sourcePicker.value = selectedSource;
    status.textContent = models.models.length ? '从图片、知识库原件或说明生成配置；核对下方 Markdown 后导入。' : '请先配置一个可用的问答模型。';
    dgUpdateGenerationCapability();
    await dgDiscoverLatestResult(own, signal);
    if (own !== dgGeneratorEpoch || !dgGeneratorOpen()) return;
    if (dgGeneratorJob) {
      if (!dgGenerationContext) dgGenerationContext = {markdown:document.getElementById('dg-md-text').value,
        body:dgGenerationRequestBody(),restored:true};
      await dgFollowJob(dgGeneratorJob.id);
    }
    else if (dgSubmittingJob) status.textContent = '正在提交后台生成，可关闭窗口。';
  } catch (error) { if (own === dgGeneratorEpoch) status.textContent = error.message; }
}
async function dgDiscoverLatestResult(operation, signal) {
  const button = document.getElementById('dg-ai-restore-result');
  dgLatestResultJob = null;
  if (!button) return;
  button.hidden = true;
  try {
    const result = await dgJobRequest('/latest', {signal});
    if (!dgGenerationIsCurrent(operation) || !dgGeneratorOpen() || signal.aborted || !result?.job) return;
    const job = result.job, workbench = window.RESEARCH_CONTEXT?.workbench;
    dgCheckJob(job);
    if (job.purpose !== 'diagram' || !workbench || job.workbench_id !== workbench || job.state !== 'succeeded' || job.result_available !== true) return;
    dgLatestResultJob = job;button.hidden = false;
  } catch { /* A missing temporary result never blocks the diagram editor. */ }
}
async function dgViewLatestResult() {
  if (!dgLatestResultJob || dgSubmittingJob || dgJobActive()) return;
  const workbench = window.RESEARCH_CONTEXT?.workbench;
  try { dgCheckJob(dgLatestResultJob); } catch (error) { document.getElementById('dg-ai-status').textContent = error.message;return; }
  if (dgLatestResultJob.purpose !== 'diagram' || !workbench || dgLatestResultJob.workbench_id !== workbench) return;
  if ((document.getElementById('dg-md-text').value.trim() || document.getElementById('dg-ai-instructions').value.trim() || dgGeneratorImage)
      && !window.confirm('查看最新暂存结果将替换当前 Markdown 草稿和生成说明。是否继续？')) return;
  dgGenerationContext = {markdown:document.getElementById('dg-md-text').value,body:dgGenerationRequestBody(),restored:true};
  dgGeneratorJob = dgLatestResultJob;
  await dgFollowJob(dgLatestResultJob.id);
}
function dgUpdateGenerationCapability() {
  const model = document.getElementById('dg-ai-model'), source = document.getElementById('dg-ai-source');
  const hasImage = !!dgGeneratorImage || source.selectedOptions[0]?.dataset.kind === 'image';
  const unsupported = hasImage && model.selectedOptions[0]?.dataset.vision !== 'true';
  document.getElementById('dg-ai-run').disabled = !model.value || unsupported || dgSubmittingJob || !!dgJobActive();
  const rebuild = document.getElementById('dg-ai-rebuild');
  if (rebuild) rebuild.disabled = document.getElementById('dg-ai-run').disabled;
  if (unsupported) document.getElementById('dg-ai-status').textContent = '当前模型不支持图片，请选择带“视觉”标识的模型，或改用文字原件与结构说明。';
  return !unsupported;
}
function dgGenerationInputChanged(input) {
  dgCancelGeneration();
  if (input?.id === 'dg-ai-source' && input.selectedOptions[0]?.dataset.kind === 'image' && dgGeneratorImage) {
    dgGeneratorImage = null;
    document.getElementById('dg-ai-image').value = '';
    document.getElementById('dg-ai-attachment').hidden = true;
  }
  document.getElementById('dg-ai-status').textContent = '从图片、知识库原件或说明生成配置；核对下方 Markdown 后导入。';
  dgUpdateGenerationCapability();
  if (dgGeneratorJob && dgGeneratorOpen()) void dgFollowJob(dgGeneratorJob.id);
}
function dgClearImage() {
  dgCancelGeneration();
  dgGeneratorImage = null;
  document.getElementById('dg-ai-image').value = '';
  document.getElementById('dg-ai-attachment').hidden = true;
  dgGenerationInputChanged();
}
async function dgChooseImage(input) {
  const file = input.files[0];
  if (!file) return;
  const status = document.getElementById('dg-ai-status');
  if (file.size > 5 * 1024 * 1024 || !['image/png','image/jpeg','image/webp','image/gif'].includes(file.type)) {
    dgClearImage();status.textContent = '请选择5 MiB以内的PNG、JPEG、WebP或GIF图片。';return;
  }
  const own = dgCancelGeneration();
  try {
    const value = await new Promise((resolve,reject) => {
      const reader = new FileReader();reader.onload = () => resolve(reader.result);reader.onerror = reject;reader.readAsDataURL(file);
    });
    if (own !== dgGeneratorEpoch) return;
    dgGeneratorImage = value;
    document.querySelector('#dg-ai-attachment span').textContent = file.name;
    document.getElementById('dg-ai-attachment').hidden = false;
    document.getElementById('dg-ai-source').value = '';
    dgUpdateGenerationCapability();
  } catch { status.textContent = '图片读取失败，请重新选择。'; }
}
async function dgGenerate(rebuild = false) {
  const button = document.getElementById('dg-ai-run'), status = document.getElementById('dg-ai-status');
  if (button.disabled) return;
  const model = document.getElementById('dg-ai-model').value;
  if (!model) { status.textContent = '请先选择生成模型。';return; }
  if (!dgUpdateGenerationCapability()) return;
  if (dgSubmittingJob || dgJobActive()) return;
  if (rebuild === true && !window.confirm('重新分析原件会重新选择结构和指标，结果可能与当前版本不同。已有保存布局不会改变，失败时保留上次成功结果。是否继续？')) return;
  const inputBody = dgGenerationRequestBody();
  const body = rebuild === true ? JSON.stringify({...JSON.parse(inputBody),rebuild:true}) : inputBody;
  if (!dgSubmission || dgSubmission.body !== body) {
    const bytes = new Uint8Array(16);window.crypto.getRandomValues(bytes);
    dgSubmission = {body,key:Array.from(bytes,value=>value.toString(16).padStart(2,'0')).join('')};
  }
  const submission = dgSubmission;
  dgCancelGeneration(); dgGeneratorJob = null; dgGenerationResult = null;
  dgGenerationContext = {markdown:document.getElementById('dg-md-text').value,
    instructions:document.getElementById('dg-ai-instructions').value,body:inputBody};
  dgSubmittingJob = true;button.disabled = true;status.textContent = '正在提交后台生成，可关闭窗口。';
  try {
    // Closing the drawer may abort metadata reads, but must not cancel an accepted server task.
    const job = await dgJobRequest('',{method:'POST',headers:{'Content-Type':'application/json','Idempotency-Key':submission.key},body:submission.body});
    dgCheckJob(job);dgGeneratorJob = job;dgSubmission = null;dgAnnounceJob(job);
    if (dgGeneratorOpen()) await dgFollowJob(job.id);
  } catch (error) { if (dgGeneratorOpen()) status.textContent = error.message; }
  finally { dgSubmittingJob = false;dgUpdateGenerationCapability(); }
}
function dgCheckJob(job) {
  if (!dgJobId(job?.id) || job.purpose && job.purpose !== 'diagram'
      || !['queued','running','succeeded','failed','interrupted','expired','applied'].includes(job.state)
      || job.workbench_id && window.RESEARCH_CONTEXT?.workbench && job.workbench_id !== window.RESEARCH_CONTEXT.workbench)
    throw new Error('后台任务范围已改变，请重新打开工作台');
}
function dgPresentGeneration(data, force = false) {
  if (typeof data?.markdown !== 'string' || !data.markdown.trim() || typeof data.version !== 'string' || !Array.isArray(data.bindings))
    throw new Error('生成结果不完整，请重新打开任务');
  dgGenerationResult = data;
  const status = document.getElementById('dg-ai-status');
  if (!force && dgGenerationContext && (document.getElementById('dg-md-text').value !== dgGenerationContext.markdown
      || dgGenerationRequestBody() !== dgGenerationContext.body)) {
    status.textContent = '后台生成已完成，当前编辑有改动，本次结果未覆盖编辑内容。';
    const restore = document.createElement('button');restore.type = 'button';restore.textContent = '查看生成结果';
    restore.className = 'ml-2 text-[var(--primary)] underline';restore.addEventListener('click',()=>dgPresentGeneration(dgGenerationResult,true));
    status.append(restore);return;
  }
  if (dgGenerationContext?.restored && data.request) {
    const model=document.getElementById('dg-ai-model'),source=document.getElementById('dg-ai-source');
    if ([...model.options].some(option=>option.value===data.request.model_id)) model.value=data.request.model_id;
    if ([...source.options].some(option=>option.value===data.request.source_id)) source.value=data.request.source_id;
    if (typeof data.request.instructions==='string') document.getElementById('dg-ai-instructions').value=data.request.instructions;
  }
  document.getElementById('dg-md-text').value = data.markdown;
  dg.generatedVersion = data.version;
  const detail = document.getElementById('dg-ai-bindings'), list = detail.querySelector('ul');list.replaceChildren();
  data.bindings.forEach(binding=>{
    const item=document.createElement('li');item.textContent=`${binding.node} → ${binding.name}（${binding.unit || '单位未注明'}）`;list.append(item);
  });
  detail.hidden = data.bindings.length === 0;
  status.textContent = (data.analysis_reused ? '已复用相同资料的研究结构。' : '') +
    `已生成 ${data.nodes} 个节点，绑定 ${data.bindings.length} 个真实指标。请核对下方配置后导入。` +
    (data.candidates_truncated ? ' 当前仅提供部分召回候选，可补充口径要求后重试。' : '');
  document.getElementById('dg-md-msg').textContent = '生成结果尚未保存；导入为新布局，或确认后应用到当前布局。';
  dgGenerationContext={markdown:data.markdown,body:dgGenerationRequestBody()};
}
async function dgFollowJob(identifier) {
  if (!dgJobId(identifier)) return;
  dgStopJobMonitor();const epoch=dgJobEpoch;
  dgJobReadController = new AbortController();const signal=dgJobReadController.signal;
  const current=()=>epoch===dgJobEpoch && dgGeneratorOpen() && dgGeneratorJob?.id===identifier;
  async function poll() {
    if (!current()) return;
    const status=document.getElementById('dg-ai-status');
    try {
      const job=await dgJobRequest('/'+encodeURIComponent(identifier),{signal});
      if (!current()) return;
      dgCheckJob(job);dgGeneratorJob=job;
      if (['queued','running'].includes(job.state)) {
        const labels={queued:'等待生成',reading_source:'正在读取原件',loading_catalog:'正在读取指标目录',structuring:'正在梳理研究主干',selecting:'正在匹配指标',generating:'正在补充指标与细节',reusing:'正在恢复已校验结构',validating:'正在核对覆盖与配置'};
        status.textContent=(labels[job.phase] || '正在后台生成')+'，可关闭窗口。';
        dgJobTimer=setTimeout(poll,2000);
      } else if (job.state==='succeeded') {
        const result=await dgJobRequest('/'+encodeURIComponent(identifier)+'/result',{signal});
        if (!current()) return;
        dgPresentGeneration(result);dgAnnounceJob(job);
      } else {
        status.textContent=job.message || ({failed:'生成未完成，请核对资料后重试。',interrupted:'生成已中断，请重新发起。',expired:'结果已过期，请重新生成。',applied:'配置已导入。'}[job.state] || '任务暂不可用，请重新打开。');
        dgAnnounceJob(job);
      }
    } catch (error) { if (current() && error.name!=='AbortError') status.textContent=error.message; }
    finally { if (current()) dgUpdateGenerationCapability(); }
  }
  return poll();
}
document.addEventListener('keydown',event => { if (event.key === 'Escape') dgCancelGeneration(); });
document.getElementById('dg-ai-restore-result')?.addEventListener('click',dgViewLatestResult);
const dgGeneratorDrawer = document.getElementById('dg-md-drawer');
if (dgGeneratorDrawer) new MutationObserver(() => {
  if (dgGeneratorDrawer.classList.contains('hidden') || dgGeneratorDrawer.classList.contains('translate-x-full')) dgCancelGeneration();
}).observe(dgGeneratorDrawer,{attributes:true,attributeFilter:['class']});
window.addEventListener('pagehide',dgCancelGeneration);
window.addEventListener('pageshow',()=>{if(dgGeneratorJob&&dgGeneratorOpen())void dgFollowJob(dgGeneratorJob.id);});
async function dgRestoreGeneration() {
  const identifier=dgJobId(new URL(location.href).searchParams.get('analysis_job'));
  if (!identifier || !document.getElementById('dg-generator')) return;
  dgGeneratorJob={id:identifier,purpose:'diagram'};dgGenerationContext=null;
  await dgOpenMarkdown();
}
if(document.readyState==='loading')document.addEventListener('DOMContentLoaded',dgRestoreGeneration,{once:true});
else void dgRestoreGeneration();
