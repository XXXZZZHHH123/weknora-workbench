export function spotlightDocument(frame: HTMLIFrameElement | undefined, fallback: Document): Document | null {
  try { return frame ? frame.contentDocument : fallback } catch { return null }
}
export function spotlightRect(element: HTMLElement, frame?: HTMLIFrameElement): DOMRect | null {
  const rect = element.getBoundingClientRect()
  if (!frame) return rect
  if (spotlightDocument(frame, element.ownerDocument) !== element.ownerDocument || !frame.clientWidth || !frame.clientHeight) return null
  const outer = frame.getBoundingClientRect(), sx = outer.width / frame.clientWidth, sy = outer.height / frame.clientHeight
  const left = Math.max(outer.left, outer.left + rect.left * sx), top = Math.max(outer.top, outer.top + rect.top * sy)
  const right = Math.min(outer.right, outer.left + rect.right * sx), bottom = Math.min(outer.bottom, outer.top + rect.bottom * sy)
  return right > left && bottom > top ? { x:left,y:top,left,top,right,bottom,width:right-left,height:bottom-top,toJSON:()=>({}) } as DOMRect : null
}

type Box = Pick<DOMRect, 'left' | 'top' | 'width' | 'height'> | null
export function sameBox(a: Box, b: Box) {
  if (!a || !b) return a === b
  return Math.abs(a.left - b.left) < 0.5 && Math.abs(a.top - b.top) < 0.5
    && Math.abs(a.width - b.width) < 0.5 && Math.abs(a.height - b.height) < 0.5
}
/**
 * Keep re-measuring a highlighted control until it stops moving. Drawers and dialogs slide in with CSS
 * transforms (often only after a request finishes), and transforms fire no scroll or resize events, so a
 * single measurement can freeze the spotlight half-way through the animation.
 */
export function followUntilStable<T extends Box>(initial: T, measure: () => T | undefined, apply: (box: T) => void,
  schedule: (callback: () => void) => number, cancel: (handle: number) => void,
  { stableFrames = 8, maxFrames = 180 } = {}) {
  let last = initial, still = 0, frames = 0, handle = 0, stopped = false
  const tick = () => {
    if (stopped) return
    const box = measure()
    if (box === undefined) { stopped = true; return }
    frames++
    if (sameBox(box, last)) still++
    else { still = 0; last = box; apply(box) }
    if (still >= stableFrames || frames >= maxFrames) { stopped = true; return }
    handle = schedule(tick)
  }
  handle = schedule(tick)
  return () => { if (!stopped) { stopped = true; cancel(handle) } }
}
