// Refresh notifications keep Pinia and embedded clients on the same credentials.
type Tokens = { token: string; refreshToken: string }
const listeners = new Set<(tokens: Tokens) => void>()
export function subscribeAuthTokens(listener: (tokens: Tokens) => void) {
  listeners.add(listener)
  return () => { listeners.delete(listener) }
}
export function publishAuthTokens(tokens: Tokens) {
  for (const listener of listeners) listener(tokens)
}
