/** Database timestamps are UTC; platform wall times are Asia/Shanghai. */
let serverOffsetMinutes = 480
let serverClockDelta = 0

export function configureServerTime(isoTime?: string, offset?: string): void {
  if (offset !== undefined && offset !== '') {
    const minutes = Number(offset)
    if (Number.isFinite(minutes) && Math.abs(minutes) <= 14 * 60) serverOffsetMinutes = minutes
  }
  if (isoTime) {
    const timestamp = Date.parse(isoTime)
    if (Number.isFinite(timestamp)) serverClockDelta = timestamp - Date.now()
  }
}

export function parseDateTime(value?: string | number | Date | null, source: 'utc' | 'platform' = 'utc'): Date | null {
  if (value === null || value === undefined || value === '') return null
  let normalized = value
  if (typeof normalized === 'string') {
    normalized = normalized.trim()
    if (/^\d{10,13}$/.test(normalized)) {
      normalized = Number(normalized) * (normalized.length === 10 ? 1000 : 1)
    } else if (/^\d{4}-\d{2}-\d{2}[ T]\d{2}:\d{2}(?::\d{2}(?:\.\d+)?)?$/.test(normalized)) {
      normalized = normalized.replace(' ', 'T') + (source === 'platform' ? '+08:00' : 'Z')
    }
  }
  const date = normalized instanceof Date ? normalized : new Date(normalized)
  return Number.isNaN(date.getTime()) ? null : date
}

function serverWallTime(value: Date): Date {
  return new Date(value.getTime() + serverOffsetMinutes * 60000)
}

export function formatDateTime(value?: string | number | Date | null): string {
  const date = parseDateTime(value)
  if (!date) return '-'
  return serverWallTime(date).toLocaleString('zh-CN', {
    timeZone: 'UTC', year: 'numeric', month: '2-digit', day: '2-digit',
    hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false,
  })
}

export function formatPlatformDateTime(value?: string | number | Date | null): string {
  return formatDateTime(parseDateTime(value, 'platform'))
}

export function getServerDateInputValue(value: Date = new Date(Date.now() + serverClockDelta)): string {
  return serverWallTime(value).toISOString().slice(0, 10)
}

// Keep existing callers compatible while using the server's actual time zone.
export const getBeijingDateInputValue = getServerDateInputValue

export function formatChatTime(timestamp: number): string {
  if (!timestamp) return ''
  const wall = serverWallTime(new Date(timestamp))
  const time = wall.toLocaleTimeString('zh-CN', { timeZone: 'UTC', hour: '2-digit', minute: '2-digit', hour12: false })
  if (getServerDateInputValue(new Date(timestamp)) === getServerDateInputValue()) return time
  return wall.toLocaleDateString('zh-CN', { timeZone: 'UTC', month: '2-digit', day: '2-digit' }) + ' ' + time
}
