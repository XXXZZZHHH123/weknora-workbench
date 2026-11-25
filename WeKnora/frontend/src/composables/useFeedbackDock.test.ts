import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test, { type TestContext } from 'node:test'
import { createRenderer, shallowRef } from 'vue'
import { useFeedbackDock } from './useFeedbackDock'

const userDock = 'weknora:feedback-dock:v1:17:alice'
const otherDock = 'weknora:feedback-dock:v1:18:alice'
const renderer = createRenderer({
  patchProp() {}, insert() {}, remove() {}, setText() {}, setElementText() {},
  createElement: () => ({}), createText: () => ({}), createComment: () => ({}),
  parentNode: () => null, nextSibling: () => null,
})

function fixture(t: TestContext, stored = new Map<string, string>()) {
  const previousWindow = globalThis.window
  t.mock.timers.enable({ apis: ['setTimeout'] })
  const resize = new Set<() => void>()
  const mediaChange = new Set<() => void>()
  const media = {
    matches: false,
    addEventListener: (_: string, fn: () => void) => mediaChange.add(fn),
    removeEventListener: (_: string, fn: () => void) => mediaChange.delete(fn),
  }
  let unavailable = false
  globalThis.window = {
    matchMedia: (query: string) => {
      assert.equal(query, '(max-width: 768px)')
      return media
    },
    addEventListener: (_: string, fn: () => void) => resize.add(fn),
    removeEventListener: (_: string, fn: () => void) => resize.delete(fn),
    get localStorage() {
      if (unavailable) throw new Error('storage disabled')
      return {
        getItem: (key: string) => stored.get(key) ?? null,
        setItem: (key: string, value: string) => stored.set(key, value),
        removeItem: (key: string) => stored.delete(key),
      }
    },
  } as unknown as Window & typeof globalThis
  const rail = { clientHeight: 676 }
  let captures = 0
  let opened = 0
  let captured: number | null = null
  const button = {
    offsetHeight: 42,
    setPointerCapture(id: number) { captured = id; captures++ },
    hasPointerCapture(id: number) { return id === captured },
    releasePointerCapture() { captured = null },
  }
  const key = shallowRef<string | null>(userDock)
  let controls!: ReturnType<typeof useFeedbackDock>
  const app = renderer.createApp({ setup() {
    controls = useFeedbackDock(shallowRef(rail as HTMLElement), shallowRef(button as unknown as HTMLButtonElement),
      key, () => { opened++ })
    return () => null
  } })
  app.mount({})
  t.after(() => {
    app.unmount()
    globalThis.window = previousWindow
    assert.equal(captured, null)
    assert.equal(resize.size + mediaChange.size, 0)
  })
  return {
    ...controls, stored, key,
    get opened() { return opened },
    get captures() { return captures },
    get captured() { return captured },
    height(value: number) { rail.clientHeight = value; resize.forEach(fn => fn()) },
    narrow(value: boolean) { media.matches = value; mediaChange.forEach(fn => fn()) },
    disableStorage() { unavailable = true },
    timer() { t.mock.timers.tick(1000) },
  }
}

function pointer(clientX: number, clientY: number) {
  return { isPrimary: true, button: 0, pointerId: 9, clientX, clientY } as PointerEvent
}

function click(detail = 1) {
  return { detail } as MouseEvent
}

function altArrow(key: string, altKey = true) {
  let prevented = false
  return {
    event: { altKey, key, preventDefault() { prevented = true } } as KeyboardEvent,
    get prevented() { return prevented },
  }
}

test('pointer movement through five pixels still opens feedback, while a drag captures the pointer and suppresses the click', t => {
  const f = fixture(t)
  f.startDrag(pointer(950, 650))
  assert.equal(f.captured, 9)
  f.moveDrag(pointer(947, 654))
  assert.equal(f.dragging.value, false)
  f.finishDrag(pointer(947, 654))
  f.click(click())
  f.timer()
  assert.equal(f.opened, 1)
  assert.equal(f.stored.size, 0)

  f.startDrag(pointer(950, 650))
  f.moveDrag(pointer(950, 644))
  assert.equal(f.dragging.value, true)
  f.finishDrag(pointer(950, 640))
  f.click(click())
  f.timer()
  assert.equal(f.opened, 1)
  assert.equal(f.captured, null)
  assert.equal(f.stored.get(userDock), '624')
})

test('the dock stays attached to the right, clamps vertically on resize, and stacks tasks on the roomier side', t => {
  const f = fixture(t)
  f.startDrag(pointer(950, 650))
  f.finishDrag(pointer(175, -100))
  assert.deepEqual(f.positionStyle.value, { '--feedback-dock-top': '0px' })
  const launcher = readFileSync(new URL('../components/FeedbackLauncher.vue', import.meta.url), 'utf8')
  assert.match(launcher, /\.feedback-dock \{[^}]+position: fixed;[^}]+right: 24px;/)
  assert.equal(f.opensDown.value, true)

  f.startDrag(pointer(950, 10))
  f.finishDrag(pointer(1650, 999))
  assert.deepEqual(f.positionStyle.value, { '--feedback-dock-top': '634px' })
  assert.equal(f.opensDown.value, false)
  f.height(351)
  assert.deepEqual(f.positionStyle.value, { '--feedback-dock-top': '309px' })
  assert.equal(f.stored.get(userDock), '634')
})

test('restoration uses the active tenant and user and falls back for invalid or blocked storage', t => {
  const f = fixture(t, new Map([[userDock, '225'], [otherDock, '810']]))
  assert.deepEqual(f.positionStyle.value, { '--feedback-dock-top': '225px' })
  f.key.value = otherDock
  assert.deepEqual(f.positionStyle.value, { '--feedback-dock-top': '634px' })
  f.key.value = 'weknora:feedback-dock:v1:18:bob'
  assert.equal(f.positionStyle.value, undefined)
  f.stored.set(otherDock, 'Infinity')
  f.key.value = otherDock
  assert.equal(f.positionStyle.value, undefined)
  f.disableStorage()
  f.key.value = userDock
  assert.equal(f.positionStyle.value, undefined)
  f.moveWithKeyboard(altArrow('ArrowUp').event)
  assert.deepEqual(f.positionStyle.value, { '--feedback-dock-top': '586px' })
})

test('Alt and the arrow keys move the handle in steps of 48 pixels and save the position', t => {
  const f = fixture(t)
  const up = altArrow('ArrowUp')
  f.moveWithKeyboard(up.event)
  assert.equal(up.prevented, true)
  assert.equal(f.stored.get(userDock), '586')
  f.moveWithKeyboard(up.event)
  assert.equal(f.stored.get(userDock), '538')
  f.moveWithKeyboard(altArrow('ArrowDown').event)
  assert.equal(f.stored.get(userDock), '586')
  const plain = altArrow('ArrowUp', false)
  f.moveWithKeyboard(plain.event)
  assert.equal(plain.prevented, false)
  assert.equal(f.stored.get(userDock), '586')
  f.click(click(0))
  assert.equal(f.opened, 1)
})

test('double-click clears the saved position and leaves feedback closed', t => {
  const f = fixture(t, new Map([[userDock, '225']]))
  f.click(click())
  f.click(click(2))
  f.reset()
  f.timer()
  assert.equal(f.opened, 0)
  assert.equal(f.positionStyle.value, undefined)
  assert.equal(f.stored.has(userDock), false)
})

test('pointer cancellation and the mobile breakpoint leave a saved desktop position intact', t => {
  const f = fixture(t, new Map([[userDock, '225']]))
  f.startDrag(pointer(950, 235))
  f.moveDrag(pointer(920, 115))
  f.cancelDrag(pointer(920, 115))
  assert.deepEqual(f.positionStyle.value, { '--feedback-dock-top': '225px' })
  assert.equal(f.stored.get(userDock), '225')

  f.narrow(true)
  assert.equal(f.positionStyle.value, undefined)
  f.startDrag(pointer(950, 235))
  f.finishDrag(pointer(920, 115))
  assert.equal(f.captures, 1)
  f.moveWithKeyboard(altArrow('ArrowUp').event)
  f.reset()
  f.click(click())
  assert.equal(f.opened, 1)
  f.narrow(false)
  assert.deepEqual(f.positionStyle.value, { '--feedback-dock-top': '225px' })
})
