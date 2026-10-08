// A JSON-RPC client for the Codex app-server that the interactive client is
// already using. It never starts a server, resumes a thread or answers a
// server request: approvals stay with the interactive client.

export class AdapterError extends Error {}

export async function connectCodex(origin, { signal, timeout = 10_000 } = {}) {
  const url = new URL(origin)
  if (url.protocol !== 'ws:' || !['127.0.0.1', '[::1]', 'localhost'].includes(url.hostname) ||
      url.username || url.password || url.search || url.hash || url.pathname !== '/') {
    throw new AdapterError('TASKBOARD_CODEX_URL must be a loopback WebSocket origin such as ws://127.0.0.1:4500')
  }
  const socket = new WebSocket(url)
  const pending = new Map()
  const listeners = new Set()
  let next = 0
  let closed = false
  const failure = () => new AdapterError('Codex app-server connection closed')
  function close() {
    if (closed) return
    closed = true
    for (const entry of pending.values()) { clearTimeout(entry.timer); entry.reject(failure()) }
    pending.clear()
    socket.close()
    for (const listener of listeners) listener({ method: 'connection/closed' })
  }
  signal?.addEventListener('abort', close, { once: true })
  socket.addEventListener('close', close)
  socket.addEventListener('error', close)
  socket.addEventListener('message', event => {
    if (typeof event.data !== 'string' || event.data.length > 16 * 1024 * 1024) { close(); return }
    let message
    try { message = JSON.parse(event.data) } catch { close(); return }
    if (message.method) {
      // Server requests (approvals, elicitations) belong to the interactive client.
      if (message.id === undefined) for (const listener of listeners) listener(message)
      return
    }
    const entry = pending.get(message.id)
    if (!entry) return
    pending.delete(message.id)
    clearTimeout(entry.timer)
    if (message.error) {
      const error = new AdapterError(`Codex rejected ${entry.method} (${message.error.code}): ${message.error.message ?? ''}`.trim())
      error.code = message.error.code
      entry.reject(error)
    } else entry.resolve(message.result)
  })
  await new Promise((resolve, reject) => {
    const timer = setTimeout(() => { close(); reject(failure()) }, timeout)
    socket.addEventListener('open', () => { clearTimeout(timer); resolve() }, { once: true })
    socket.addEventListener('close', () => { clearTimeout(timer); reject(failure()) }, { once: true })
  })
  const call = (method, params, callTimeout = timeout) => new Promise((resolve, reject) => {
    if (closed) { reject(failure()); return }
    const id = ++next
    const timer = setTimeout(() => {
      pending.delete(id)
      reject(new AdapterError(`Codex ${method} timed out`))
    }, callTimeout)
    pending.set(id, { resolve, reject, timer, method })
    try { socket.send(JSON.stringify({ id, method, params })) } catch { pending.delete(id); clearTimeout(timer); reject(failure()) }
  })
  try {
    await call('initialize', { clientInfo: { name: 'taskboard_delivery', version: '0.1.0' }, capabilities: { experimentalApi: true } })
    socket.send(JSON.stringify({ method: 'initialized' }))
  } catch (error) { close(); throw error }
  return {
    call,
    close,
    onNotification(listener) { listeners.add(listener); return () => listeners.delete(listener) },
  }
}
