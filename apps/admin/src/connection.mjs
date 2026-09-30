/** server timestamps are compared to server time, not the browser's clock. */
export function connectionStatus(snapshot, receivedAt, now = Date.now(), failed = false) {
  if (failed) return 'unreachable'
  if (!snapshot || !receivedAt) return 'checking'
  if (now - receivedAt > 20000) return 'stale'
  const serverTime = Date.parse(snapshot.server_time)
  const heartbeat = Date.parse(snapshot.server.heartbeat_at)
  if (!Number.isFinite(serverTime) || !Number.isFinite(heartbeat)) return 'stale'
  if (serverTime - heartbeat > 15000 || heartbeat - serverTime > 5000) return 'degraded'
  return snapshot.server.status === 'online' ? 'online' : 'degraded'
}
export function csvCell(value) {
  let text = String(value ?? '')
  // Spreadsheet formula injection protection for exported user-controlled fields.
  if (/^[\s]*[=+@-]/.test(text)) text = "'" + text
  return '"' + text.replaceAll('"', '""') + '"'
}
export function formatBytes(value) {
  if (value === null || value === undefined || !Number.isFinite(value)) return '未采集'
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB']
  let i = 0; while (value >= 1024 && i < units.length - 1) { value /= 1024; i++ }
  return `${value.toFixed(i ? 1 : 0)} ${units[i]}`
}

/** Receipt time belongs to the server. A reported clock is never a freshness authority. */
export function telemetryState(device, serverTime, healthy = true) {
 if (!healthy) return 'unknown'
 if (!device?.telemetry) return 'missing'
 if (!device.online) return 'offline'
 const received=Date.parse(device.telemetry.received_at), now=Date.parse(serverTime)
 if (!Number.isFinite(received)||!Number.isFinite(now)||received>now+5000) return 'unknown'
 return now-received>45000?'stale':'fresh'
}
