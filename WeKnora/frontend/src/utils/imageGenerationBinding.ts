const imageTool = 'generate_image'

// Called only by explicit editor actions, never while hydrating saved agents.
// Preserve inherited defaults when making the tool list explicit for the first time.
export function imageGenerationTools(
  allowed: readonly string[] | null | undefined,
  enabled: boolean,
  defaults: readonly string[] = [],
): string[] {
  const tools = (allowed ?? defaults).filter((name) => name !== imageTool)
  return enabled ? [...tools, imageTool] : tools
}
