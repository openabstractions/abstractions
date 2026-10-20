import http from "node:http"

let models = ["qwen3:8b", "phi4:14b"]
let lengths = {"qwen3:8b": 8192, "phi4:14b": 8192}
let chatStarted = 0
let releaseChat
let waitingChat
let nextToolCall
let pendingToolCall
let lastChatRequest = null
const requests = []

const server = http.createServer(async (req, res) => {
  const url = new URL(req.url ?? "/", "http://127.0.0.1")
  if (req.method === "GET" && url.pathname === "/api/tags") {
    res.writeHead(200, {"content-type": "application/json"})
    res.end(JSON.stringify({models: models.map((name) => ({name, model: name, size: 1, digest: name}))}))
    return
  }
  if (req.method === "GET" && url.pathname === "/api/ps") {
    res.writeHead(200, {"content-type": "application/json"})
    res.end(JSON.stringify({models: []}))
    return
  }
  if (req.method === "POST" && url.pathname === "/api/show") {
    let body = ""
    for await (const chunk of req) body += chunk
    const name = JSON.parse(body).model
    if (!models.includes(name)) {
      res.writeHead(404)
      res.end()
      return
    }
    res.writeHead(200, {"content-type": "application/json"})
    res.end(JSON.stringify({capabilities: ["completion"], model_info: {"general.architecture": "llama", "llama.context_length": lengths[name] ?? 8192}}))
    return
  }
  if (req.method === "POST" && url.pathname === "/v1/chat/completions") {
    let body = ""
    for await (const chunk of req) body += chunk
    const request = JSON.parse(body)
    chatStarted++
    const messages = Array.isArray(request.messages) ? request.messages : []
    const toolNames = (request.tools ?? []).map((item) => item.function?.name ?? item.name)
      .filter((name) => typeof name === "string")
    const observed = {
      ordinal: chatStarted,
      toolNames: toolNames.slice(0, 32).map((name) => name.slice(0, 64)),
      messageRoles: messages.slice(0, 64).map((message) => String(message.role).slice(0, 32)),
      toolReplyCount: messages.filter((message) => message.role === "tool").length,
    }
    lastChatRequest = observed
    requests.push(observed)
    if (requests.length > 32) requests.shift()
    const toolMessage = messages.findLast((message) => message.role === "tool")
    if (pendingToolCall && toolMessage) {
      observed.branch = "tool-reply"
      if (toolMessage.tool_call_id !== pendingToolCall.id) {
        observed.status = 409
        res.writeHead(409, {"content-type": "application/json"})
        res.end(JSON.stringify({error: "tool reply ID does not match the emitted call"}))
        return
      }
      const toolReply = toolMessage.content ?? ""
      let parsed
      try { parsed = JSON.parse(toolReply) } catch {}
      if (parsed?.kind !== "source_watch" || typeof parsed.status !== "string" || !Number.isInteger(parsed.records)) {
        observed.status = 409
        res.writeHead(409, {"content-type": "application/json"})
        res.end(JSON.stringify({error: "follow-up requires a valid source-watch result"}))
        return
      }
      pendingToolCall = undefined
      observed.status = 200
      const content = `The synthetic source watch ended with status ${parsed.status} and reported ${parsed.records} record(s).`
      res.writeHead(200, {"content-type": "text/event-stream", "cache-control": "no-cache"})
      res.write(`data: ${JSON.stringify({id: "watch-fixture", object: "chat.completion.chunk", created: 1, model: request.model, choices: [{index: 0, delta: {content}, finish_reason: null}]})}\n\n`)
      res.write(`data: ${JSON.stringify({id: "watch-fixture", object: "chat.completion.chunk", created: 1, model: request.model, choices: [{index: 0, delta: {}, finish_reason: "stop"}]})}\n\n`)
      res.end("data: [DONE]\n\n")
      return
    }
    // Title and unrelated model requests do not consume the queued tool call.
    if (nextToolCall && !pendingToolCall && toolNames.includes("openabstractions_watch_logs")) {
      const call = nextToolCall
      nextToolCall = undefined
      const id = `call-${chatStarted}`
      pendingToolCall = {id}
      observed.branch = "tool-call"
      observed.status = 200
      observed.toolCallId = id
      res.writeHead(200, {"content-type": "text/event-stream", "cache-control": "no-cache"})
      res.write(`data: ${JSON.stringify({id: "watch-fixture", object: "chat.completion.chunk", created: 1, model: request.model, choices: [{index: 0, delta: {role: "assistant"}, finish_reason: null}]})}\n\n`)
      res.write(`data: ${JSON.stringify({id: "watch-fixture", object: "chat.completion.chunk", created: 1, model: request.model, choices: [{index: 0, delta: {tool_calls: [{index: 0, id, type: "function", function: {name: "openabstractions_watch_logs", arguments: ""}}]}, finish_reason: null}]})}\n\n`)
      res.write(`data: ${JSON.stringify({id: "watch-fixture", object: "chat.completion.chunk", created: 1, model: request.model, choices: [{index: 0, delta: {tool_calls: [{index: 0, function: {arguments: JSON.stringify(call)}}]}, finish_reason: null}]})}\n\n`)
      res.write(`data: ${JSON.stringify({id: "watch-fixture", object: "chat.completion.chunk", created: 1, model: request.model, choices: [{index: 0, delta: {}, finish_reason: "tool_calls"}]})}\n\n`)
      res.end("data: [DONE]\n\n")
      return
    }
    observed.branch = "default"
    waitingChat?.()
    // Keep the old blocked stream for refresh tests. Side requests during a
    // queued tool turn answer directly, leaving its call/reply state intact.
    if (!nextToolCall && !pendingToolCall) await new Promise((resolve) => { releaseChat = resolve })
    observed.status = 200
    res.writeHead(200, {"content-type": "text/event-stream", "cache-control": "no-cache"})
    res.write(`data: ${JSON.stringify({id: "refresh-fixture", object: "chat.completion.chunk", created: 1, model: "qwen3:8b", choices: [{index: 0, delta: {content: "stream-survived-refresh"}, finish_reason: null}]})}\n\n`)
    res.write(`data: ${JSON.stringify({id: "refresh-fixture", object: "chat.completion.chunk", created: 1, model: "qwen3:8b", choices: [{index: 0, delta: {}, finish_reason: "stop"}]})}\n\n`)
    res.end("data: [DONE]\n\n")
    releaseChat = undefined
    return
  }
  if (req.method === "GET" && url.pathname === "/control/state") {
    res.writeHead(200, {"content-type": "application/json"})
    res.end(JSON.stringify({models, lengths, chatStarted, lastChatRequest, requests,
      queuedToolCall: Boolean(nextToolCall), pendingToolCallId: pendingToolCall?.id ?? null}))
    return
  }
  if (req.method === "POST" && url.pathname === "/control/models") {
    let body = ""
    for await (const chunk of req) body += chunk
    const next = JSON.parse(body)
    models = next.models
    lengths = next.lengths
    res.writeHead(200, {"content-type": "application/json"})
    res.end(JSON.stringify({models, lengths}))
    return
  }
  if (req.method === "POST" && url.pathname === "/control/tool-call") {
    let body = ""
    for await (const chunk of req) body += chunk
    const next = JSON.parse(body)
    if (!next || !["fixture/system", "fixture/gap", "fixture/unavailable"].includes(next.sourceId)) {
      res.writeHead(400)
      res.end("fixed synthetic source ID required")
      return
    }
    if (![1, 2, 3, 1000].includes(next.durationMs) || ![1, 2].includes(next.maxRecords) || next.maxBytes !== 4096 ||
        (next.shareEvents !== undefined && typeof next.shareEvents !== "boolean")) {
      res.writeHead(400)
      res.end("bounded fixture call required")
      return
    }
    if (nextToolCall || pendingToolCall) {
      res.writeHead(409)
      res.end("a synthetic tool call is already queued or awaiting its reply")
      return
    }
    nextToolCall = next
    res.writeHead(200, {"content-type": "application/json"})
    res.end(JSON.stringify({queued: true, sourceId: next.sourceId, shareEvents: next.shareEvents === true}))
    return
  }
  if (req.method === "POST" && url.pathname === "/control/wait-chat") {
    if (chatStarted > 0) {
      res.writeHead(200)
      res.end("started")
      return
    }
    waitingChat = () => { res.writeHead(200); res.end("started") }
    return
  }
  if (req.method === "POST" && url.pathname === "/control/release-chat") {
    releaseChat?.()
    res.writeHead(200)
    res.end("released")
    return
  }
  res.writeHead(404)
  res.end()
})

server.listen(Number(process.env.OA_MODEL_FIXTURE_PORT ?? 43188), "127.0.0.1", () => {
  if (process.env.OA_MODEL_FIXTURE_PORT === "0") process.stdout.write(`${server.address().port}\n`)
})
