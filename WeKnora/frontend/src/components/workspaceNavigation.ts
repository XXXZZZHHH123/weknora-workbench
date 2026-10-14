interface NamedResource {
  id: string
  name: string
}

export interface WorkspaceNavigationLeaf {
  id: string
  title: string
  path: string
}

function resourceLeaves(resources: readonly NamedResource[], basePath: string, preview = false): WorkspaceNavigationLeaf[] {
  const seen = new Set<string>()
  return resources.filter((resource) => {
    if (!resource.id || seen.has(resource.id)) return false
    seen.add(resource.id)
    return true
  }).map((resource) => ({
    id: resource.id,
    title: resource.name,
    path: `${basePath}/${encodeURIComponent(resource.id)}${preview ? '/preview' : ''}`,
  }))
}

/** Global navigation stops at the resource; directories and functions belong to its page. */
export function buildWorkspaceNavigation(
  projects: readonly NamedResource[],
  knowledgeBases: readonly NamedResource[],
  workbenches: readonly NamedResource[],
) {
  return {
    projects: resourceLeaves(projects, '/platform/projects'),
    knowledgeBases: resourceLeaves(knowledgeBases, '/platform/knowledge-bases'),
    workbenches: resourceLeaves(workbenches, '/platform/research-workbenches', true),
  }
}
