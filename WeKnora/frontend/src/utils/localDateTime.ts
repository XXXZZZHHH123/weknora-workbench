/** Keep dates supplied without a time as calendar dates. Format instants in the browser's time zone. */
export function localDateTime(value?: string | null, includeTime = true): string {
  if (!value) return ''
  if (/^\d{4}-\d{2}-\d{2}$/.test(value)) return value
  const date = new Date(value)
  if (!Number.isFinite(date.getTime())) return ''
  const pad = (n: number) => String(n).padStart(2, '0')
  const day = `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}`
  return includeTime ? `${day} ${pad(date.getHours())}:${pad(date.getMinutes())}` : day
}
