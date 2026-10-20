// OpenCode's context-overflow classifier, shared by the overflow tests.
// OpenCode 1.18.34's session processor answers a ContextOverflowError by
// compacting the conversation and continuing (session/processor.ts halt:
// needsCompaction), unless compaction.auto is false.
// OpenCode v1.18.31 classifies an APICallError as ContextOverflowError, which
// it compacts and never retries, when its message matches these patterns
// (packages/llm/src/provider-error.ts at 014614d35b397775e5d397a490fc72368c894ec2,
// MIT licence) or its body carries error.code context_length_exceeded.
export const openCodeOverflowPatterns = [
  /prompt is too long/i, /request_too_large/i, /input is too long for requested model/i, /exceeds the context window/i,
  /exceeds (?:the )?(?:model'?s )?maximum context length(?: of [\d,]+ tokens?|\s*\([\d,]+\))/i,
  /input token count.*exceeds the maximum/i, /tokens in request more than max tokens allowed/i, /maximum prompt length is \d+/i,
  /reduce the length of the messages/i, /maximum context length is \d+ tokens/i,
  /exceeds (?:the )?maximum allowed input length of [\d,]+ tokens?/i,
  /input \(\d+ tokens\) is longer than the model'?s context length \(\d+ tokens\)/i, /exceeds the limit of \d+/i,
  /exceeds the available context size/i, /greater than the context length/i, /context window exceeds limit/i,
  /exceeded model token limit/i, /context[_ ]length[_ ]exceeded/i, /request entity too large/i, /context length is only \d+ tokens/i,
  /input length.*exceeds.*context length/i, /prompt too long; exceeded (?:max )?context length/i,
  /too large for model with \d+ maximum context length/i,
  /prompt has [\d,]+ tokens?, but the configured context size is [\d,]+ tokens?/i, /model_context_window_exceeded/i,
  /too many tokens/i, /token limit exceeded/i,
];
const openCodeOverflowExclusions = [/^(throttling error|service unavailable):/i, /rate limit/i, /too many requests/i];

export function openCodeSeesOverflow(error) {
  const message = error.message;
  const body = error.responseBody ? JSON.parse(error.responseBody) : undefined;
  return (!openCodeOverflowExclusions.some((pattern) => pattern.test(message)) &&
    openCodeOverflowPatterns.some((pattern) => pattern.test(message))) ||
    error.statusCode === 413 || body?.error?.code === 'context_length_exceeded';
}

