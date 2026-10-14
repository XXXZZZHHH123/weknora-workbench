export interface WorkspaceKnowledgeBase {
  id: string
  name: string
  description?: string
  type?: string
}

export interface WorkspaceSharedKnowledgeBase {
  knowledge_base: WorkspaceKnowledgeBase | null
}

export function workspaceKnowledgeBases<T extends WorkspaceKnowledgeBase>(
  own: readonly T[],
  shared: readonly WorkspaceSharedKnowledgeBase[],
  identity: string,
  sharedIdentity: string,
): WorkspaceKnowledgeBase[] {
  const seen = new Set<string>()
  const candidates: WorkspaceKnowledgeBase[] = [...own]
  // A cached share list has no identity field; callers stamp only completed
  // reads for the current account and tenant before allowing it into choices.
  if (identity && identity === sharedIdentity) {
    for (const entry of shared) if (entry.knowledge_base) candidates.push(entry.knowledge_base)
  }
  return candidates.filter(kb => {
    if (!kb.id || seen.has(kb.id)) return false
    seen.add(kb.id)
    return true
  })
}
