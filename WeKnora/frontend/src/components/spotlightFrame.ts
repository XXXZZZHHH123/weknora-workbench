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
