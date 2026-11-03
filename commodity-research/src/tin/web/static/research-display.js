/* Metadata display only. Real IDs and revisions remain untouched in requests. */
(function () {
  const node = document.getElementById('research-display-names');
  let names = {}; try { names = JSON.parse(node?.textContent || '{}'); } catch (_) { /* No unverified names. */ }
  const field = /kb-[0-9a-f]{48}(?![0-9a-f])|generic\.[0-9a-f]{12,48}(?![0-9a-f])/g;
  const vendor = /\b(?:ID\d{5,}|[A-Za-z]\d{7,})\b/g;
  const tag = /\[\s*(?:kb-[0-9a-f]{48}|[A-Za-z][A-Za-z0-9_-]*(?:\.[A-Za-z0-9_-]+)+)\s*\]/g;
  const clean = value => String(value ?? '').replace(tag, '').replace(field, '相关指标').replace(vendor, '').split(' / ').map(x => x.trim()).filter(Boolean).join(' / ');
  function text(value) {
    let result = String(value ?? '').replace(tag, '');
    const candidates = new Set(result.match(/kb-[0-9a-f]{48}|[A-Za-z][A-Za-z0-9_-]*(?:\.[A-Za-z0-9_-]+)+/g) || []);
    for (const id of [...candidates].filter(id => Object.hasOwn(names, id)).sort((a, b) => b.length - a.length)) {
      if (id.length < 3 || !names[id]) continue;
      const escaped = id.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
      result = result.replace(new RegExp('(?<![A-Za-z0-9_.-])' + escaped + '(?![A-Za-z0-9_.-])', 'g'), clean(names[id]));
    }
    return clean(result);
  }
  function reference(row, fallback = '研究判断') {
    let title = text(row?.title);
    if (!title || /^(?:研判|研究判断)\s*(?:v\d+|#\d+)?$/.test(title)) title = fallback;
    const stamp = row?.written_at || row?.created_at || row?.updated_at;
    return title + (stamp ? ' · ' + String(stamp).slice(0, 16).replace('T', ' ') : '');
  }
  // Source layout only: a Markdown list marker or table row border is not part of the condition.
  const listMark = /^\s*(?:[-*+•·]\s+|\d{1,3}(?:[.)]\s+|、)|[（(]\d{1,3}[)）]\s*)/;
  function line(value) {
    const labelled = /^([^：:|\s]{1,8}[：:]\s*)(.*)$/.exec(value), label = labelled ? labelled[1] : '';
    let body = (labelled ? labelled[2] : value).trim().replace(listMark, '');
    if (/^\|.*\|$/.test(body)) body = body.slice(1, -1).split('|').map(cell => cell.trim()).filter(Boolean).join(' · ');
    return label + body;
  }
  function prose(value) { return text(value).split('\n').map(line).join('\n'); }
  window.ResearchDisplay = {text, prose, indicator: id => text(names[id]) || '指标名称待核对', reference};
})();
