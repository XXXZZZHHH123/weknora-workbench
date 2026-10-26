/** Keep similarly named application files and icons out of heavy vendor chunks. */
export function vendorChunk(id: string): string | undefined {
  const normalized = id.replace(/\\/g, '/').split('?')[0]!
  const packagePath = normalized.split('/node_modules/').at(-1)!
  if (packagePath === normalized) return
  const segments = packagePath.split('/')
  const packageName = segments[0]?.startsWith('@') ? segments.slice(0, 2).join('/') : segments[0]

  if (['mermaid', 'dagre', 'dagre-d3', 'dagre-d3-es', '@dagrejs/dagre', '@dagrejs/graphlib',
    'cytoscape', 'cytoscape-fcose', 'cytoscape-cose-bilkent', 'cytoscape-dagre'].includes(packageName!)) {
    return 'vendor-mermaid'
  }
  if (['marked', 'marked-katex-extension', 'katex'].includes(packageName!)) return 'vendor-markdown'
  if (packageName === 'highlight.js') return 'vendor-highlight'
}
