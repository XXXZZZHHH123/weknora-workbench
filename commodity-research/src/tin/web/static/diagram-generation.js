/* The generated draft uses the existing Markdown editor and import actions. */
let dgGeneratorImage = null, dgGeneratorEpoch = 0, dgGeneratorController = null;
function dgCancelGeneration() {
  dgGeneratorEpoch++;
  dgGeneratorController?.abort();dgGeneratorController = null;
  const button = document.getElementById('dg-ai-run');
  if (button) button.disabled = false;
  return dgGeneratorEpoch;
}
function dgGenerationIsCurrent(operation) { return operation === dgGeneratorEpoch; }
async function dgGeneratorRequest(path, options) {
  const response = await researchFetch((window.RESEARCH_PREFIX || '') + '/api/sn/diagram/generation' + path, options);
  const data = await response.json();
  if (!response.ok) throw new Error(typeof data.detail === 'string' ? data.detail : '生成未完成，请核对输入和模型配置');
  return data;
}
async function dgOpenGenerator() {
  if (await dgOpenMarkdown() === false) return;
  const own = dgGeneratorEpoch;
  dgGeneratorController = new AbortController();
  const signal = dgGeneratorController.signal;
  const panel = document.getElementById('dg-generator');
  if (!panel) return;
  panel.hidden = false;
  const status = document.getElementById('dg-ai-status');
  try {
    const [models, sources] = await Promise.all([dgGeneratorRequest('/models',{signal}),dgGeneratorRequest('/sources',{signal})]);
    if (own !== dgGeneratorEpoch) return;
    const picker = document.getElementById('dg-ai-model'), selected = picker.value;
    picker.replaceChildren();
    models.models.forEach(model => {
      const option = new Option(model.name + (model.supports_vision ? ' · 视觉' : ''),model.id);
      picker.add(option);
    });
    if ([...picker.options].some(option => option.value === selected)) picker.value = selected;
    const sourcePicker = document.getElementById('dg-ai-source');
    sourcePicker.replaceChildren(new Option('知识库原件',''));
    sources.sources.forEach(source => sourcePicker.add(new Option(source.name,source.id)));
    if (!models.models.length) status.textContent = '请先配置一个可用的问答模型。';
  } catch (error) { if (own === dgGeneratorEpoch) status.textContent = error.message; }
}
function dgClearImage() {
  dgCancelGeneration();
  dgGeneratorImage = null;
  document.getElementById('dg-ai-image').value = '';
  document.getElementById('dg-ai-attachment').hidden = true;
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
  } catch { status.textContent = '图片读取失败，请重新选择。'; }
}
async function dgGenerate() {
  const button = document.getElementById('dg-ai-run'), status = document.getElementById('dg-ai-status');
  if (button.disabled) return;
  const model = document.getElementById('dg-ai-model').value;
  if (!model) { status.textContent = '请先选择生成模型。';return; }
  const own = ++dgGeneratorEpoch;
  dgGeneratorController?.abort();dgGeneratorController = new AbortController();
  const initialMarkdown = document.getElementById('dg-md-text').value;
  const initialInstructions = document.getElementById('dg-ai-instructions').value;
  button.disabled = true;status.textContent = '正在理解资料并核对知识库指标…';
  try {
    const data = await dgGeneratorRequest('',{method:'POST',signal:dgGeneratorController.signal,headers:{'Content-Type':'application/json'},body:JSON.stringify({
      model_id:model,instructions:document.getElementById('dg-ai-instructions').value,
      image:dgGeneratorImage,source_id:document.getElementById('dg-ai-source').value || null,
    })});
    if (own !== dgGeneratorEpoch) return;
    if (document.getElementById('dg-md-text').value !== initialMarkdown || document.getElementById('dg-ai-instructions').value !== initialInstructions) {
      status.textContent = '生成期间配置或说明已修改，本次结果未覆盖编辑内容。请核对后重新生成。';return;
    }
    document.getElementById('dg-md-text').value = data.markdown;
    dg.generatedVersion = data.version;
    const detail = document.getElementById('dg-ai-bindings'), list = detail.querySelector('ul');
    list.replaceChildren();
    data.bindings.forEach(binding => {
      const item = document.createElement('li');
      item.textContent = `${binding.node} → ${binding.name}（${binding.unit || '单位未注明'}）`;
      list.append(item);
    });
    detail.hidden = data.bindings.length === 0;
    status.textContent = `已生成 ${data.nodes} 个节点，绑定 ${data.bindings.length} 个真实指标。请核对下方配置后导入。` +
      (data.candidates_truncated ? ' 当前仅提供部分召回候选，可补充口径要求后重试。' : '');
    document.getElementById('dg-md-msg').textContent = '生成结果尚未保存；导入为新布局，或确认后应用到当前布局。';
  } catch (error) { if (own === dgGeneratorEpoch) status.textContent = error.message; }
  finally { if (own === dgGeneratorEpoch) button.disabled = false; }
}
document.addEventListener('keydown',event => { if (event.key === 'Escape') dgCancelGeneration(); });
const dgGeneratorDrawer = document.getElementById('dg-md-drawer');
if (dgGeneratorDrawer) new MutationObserver(() => {
  if (dgGeneratorDrawer.classList.contains('hidden') || dgGeneratorDrawer.classList.contains('translate-x-full')) dgCancelGeneration();
}).observe(dgGeneratorDrawer,{attributes:true,attributeFilter:['class']});
