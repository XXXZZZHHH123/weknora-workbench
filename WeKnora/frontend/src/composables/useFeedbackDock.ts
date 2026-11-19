import { computed, onBeforeUnmount, onMounted, ref, watch, type Ref } from 'vue'
import { getRootZoom } from '../utils/zoom'

const DRAG_THRESHOLD = 5
const KEYBOARD_STEP = 48
// A pointer click waits for the double-click gesture on the same button.
const DOUBLE_CLICK_DELAY = 500

export function useFeedbackDock(
  rail: Ref<HTMLElement | null>,
  handle: Ref<HTMLButtonElement | null>,
  storageKey: Ref<string | null>,
  openFeedback: () => void,
) {
  const mobile = ref(false)
  const top = ref<number | null>(null)
  const maxTop = ref(0)
  const dragging = ref(false)
  let clickTimer: ReturnType<typeof setTimeout> | undefined
  let skipClick = false
  let drag: {
    id: number
    handle: HTMLButtonElement
    x: number
    y: number
    top: number
    originalTop: number | null
    zoom: number
  } | null = null

  const clamp = (value: number) => Math.min(Math.max(value, 0), maxTop.value)
  const positionStyle = computed(() => !mobile.value && top.value !== null
    ? { '--feedback-dock-top': `${clamp(top.value)}px` }
    : undefined)
  const opensDown = computed(() => !mobile.value && top.value !== null && clamp(top.value) < maxTop.value / 2)

  function clearClick() {
    clearTimeout(clickTimer)
    clickTimer = undefined
  }

  function readPosition(key: string | null): number | null {
    if (!key) return null
    try {
      const raw = window.localStorage.getItem(key)
      const value = raw?.trim() ? Number(raw) : NaN
      return Number.isFinite(value) && value >= 0 ? value : null
    } catch {
      return null
    }
  }

  function savePosition() {
    const key = storageKey.value
    if (!key) return
    try {
      if (top.value === null) window.localStorage.removeItem(key)
      else window.localStorage.setItem(key, String(top.value))
    } catch {
      // The dock remains movable while browser storage is unavailable.
    }
  }

  function measure() {
    if (rail.value && handle.value) {
      maxTop.value = Math.max(0, rail.value.clientHeight - handle.value.offsetHeight)
    }
  }

  function startDrag(event: PointerEvent) {
    const button = handle.value
    if (!button || !event.isPrimary || event.button !== 0 || drag) return
    skipClick = false
    if (mobile.value) return
    measure()
    button.setPointerCapture(event.pointerId)
    drag = {
      id: event.pointerId, handle: button, x: event.clientX, y: event.clientY,
      top: clamp(top.value ?? maxTop.value), originalTop: top.value, zoom: getRootZoom(),
    }
  }

  function moveDrag(event: PointerEvent) {
    if (!drag || event.pointerId !== drag.id) return
    const dy = event.clientY - drag.y
    if (!dragging.value && Math.hypot(event.clientX - drag.x, dy) <= DRAG_THRESHOLD) return
    clearClick()
    dragging.value = true
    top.value = clamp(drag.top + dy / drag.zoom)
  }

  function stopDrag(commit: boolean) {
    if (!drag) return
    const { id, handle: button, originalTop } = drag
    drag = null
    if (dragging.value) {
      skipClick = true
      if (commit) savePosition()
      else top.value = originalTop
      dragging.value = false
    }
    if (button.hasPointerCapture(id)) button.releasePointerCapture(id)
  }

  function finishDrag(event: PointerEvent) {
    if (event.pointerId !== drag?.id) return
    moveDrag(event)
    stopDrag(true)
  }

  function cancelDrag(event: PointerEvent) {
    if (event.pointerId === drag?.id) stopDrag(false)
  }

  function click(event: MouseEvent) {
    if (skipClick && event.detail > 0) {
      skipClick = false
      return
    }
    clearClick()
    if (mobile.value || event.detail === 0) openFeedback()
    else if (event.detail === 1) clickTimer = setTimeout(openFeedback, DOUBLE_CLICK_DELAY)
  }

  function reset() {
    if (mobile.value || skipClick) return
    clearClick()
    top.value = null
    savePosition()
  }

  function moveWithKeyboard(event: KeyboardEvent) {
    if (mobile.value || !event.altKey || (event.key !== 'ArrowUp' && event.key !== 'ArrowDown')) return
    event.preventDefault()
    measure()
    const previous = clamp(top.value ?? maxTop.value)
    const next = clamp(previous + (event.key === 'ArrowUp' ? -KEYBOARD_STEP : KEYBOARD_STEP))
    if (next === previous) return
    top.value = next
    savePosition()
  }

  watch(storageKey, key => {
    stopDrag(false)
    clearClick()
    top.value = readPosition(key)
  }, { immediate: true, flush: 'sync' })

  const query = typeof window === 'undefined' ? null : window.matchMedia('(max-width: 768px)')
  function syncMobile() {
    mobile.value = !!query?.matches
    if (mobile.value) stopDrag(false)
    measure()
  }

  onMounted(() => {
    syncMobile()
    query?.addEventListener('change', syncMobile)
    window.addEventListener('resize', measure)
  })
  watch([rail, handle], ([element, button], _, onCleanup) => {
    if (!element || !button) {
      stopDrag(false)
      clearClick()
      return
    }
    measure()
    if (typeof ResizeObserver === 'undefined') return
    const observer = new ResizeObserver(measure)
    observer.observe(element)
    onCleanup(() => observer.disconnect())
  }, { flush: 'post' })
  onBeforeUnmount(() => {
    stopDrag(false)
    clearClick()
    query?.removeEventListener('change', syncMobile)
    window.removeEventListener('resize', measure)
  })

  return { mobile, dragging, positionStyle, opensDown, startDrag, moveDrag, finishDrag, cancelDrag, click, reset, moveWithKeyboard }
}
