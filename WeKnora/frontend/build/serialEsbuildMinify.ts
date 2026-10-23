import type { Plugin } from 'vite'

export function serialEsbuildMinify(): Plugin {
  return {
    name: 'serial-esbuild-minify',
    apply: 'build',
    configResolved(config) {
      if (config.esbuild === false || config.build.minify !== 'esbuild') return

      const matches = config.plugins.filter((plugin) => plugin.name === 'vite:esbuild-transpile')
      if (matches.length !== 1 || !matches[0]?.renderChunk) {
        throw new Error('serial-esbuild-minify requires one Vite esbuild renderChunk hook')
      }

      const target = matches[0]
      const hook = target.renderChunk!
      const original = typeof hook === 'function' ? hook : hook.handler
      if (typeof original !== 'function') {
        throw new Error('serial-esbuild-minify requires a callable Vite esbuild renderChunk hook')
      }

      // Reuse Vite's transform to keep its target, minify options and source maps intact.
      let pending = Promise.resolve()
      const handler: typeof original = function (...args) {
        const result = pending.then(() => original.apply(this, args))
        // A failed chunk must reject its caller without poisoning subsequent calls.
        pending = result.then(() => undefined, () => undefined)
        return result
      }
      target.renderChunk = typeof hook === 'function' ? handler : { ...hook, handler }
    },
  }
}
