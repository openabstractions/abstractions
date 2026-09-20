namespace * abstraction.inference.api

// Model calls the runtime performs for an application: messages, parts, tools,
// usage and typed refusals, independent of any vendor wire. The service picks
// a host through the router, applies a named credential to hosted requests
// itself, and streams the reply as bounded pages of deltas.
encoding json {
 escape="minimal"
 indent="2"
 map_keys="utf8-bytes"
 numbers="integer-decimal"
 opaque="verbatim"
 terminator="newline"
 duplicate_keys="refuse"
 depth_limit="64"
}
refusal {
 1: malformed(stage="grammar")
 2: bad_string(stage="grammar")
 3: bad_binary(stage="grammar")
 4: number_spelling(stage="grammar")
 5: wrong_type(stage="grammar")
 6: depth_exceeded(stage="grammar")
 7: duplicate_key(stage="grammar")
 8: duplicate_field(stage="structure")
 9: unknown_field(stage="structure")
 10: missing_field(stage="structure")
 11: bad_enum(stage="structure")
 12: trailing_bytes(stage="document")
}

// Guarantees a request states. Without hosted-allowed@1 only hosts on the
// receiving machine serve it; local-only@1 says so explicitly, and a request
// naming both is invalid. A hosted host serves a request only when the request
// also names that host's credential. The member identifiers are source names;
// their wire annotations preserve the published guarantee words. The codec
// carries future words to the service, which validates every guarantee and
// returns invalid before selecting a host or performing an upstream request.
enum RequestGuarantee {
 1: local_only(wire="abstraction.inference/local-only@1")
 2: hosted_allowed(wire="abstraction.inference/hosted-allowed@1")
}(unknown="grant",reader="validate")
// Request features a host may lack. A host that lacks one the request uses is
// refused as unsupported_feature with reason feature:<name> before any request
// leaves the service. A code provider declares the features it serves.
const list<string> features = ["abstraction.inference/tools@1", "abstraction.inference/json-schema@1", "abstraction.inference/vision@1"]
// Rights actions this capability enforces. complete is decided on resource
// host:<name> for the bound caller before any upstream request; host.manage,
// key.issue and audit.read are decided on resource account for the operator@1
// host, key and audit calls. Closed by INF-R1; the next name this list would
// reserve is abstraction.inference/budget.manage.
const list<string> resource_actions = ["abstraction.inference/complete", "abstraction.inference/host.manage", "abstraction.inference/key.issue", "abstraction.inference/audit.read"] (catalogue = "closed", closed_by = "INF-R1")
// The credentials kind a local key minted for the gateway window is held as.
// The holder never applies it to an outgoing request; the window verifies a
// presented key against it.
const list<string> local_key_kinds = ["openabstractions/local-key@1"]
// Local host kinds operator@1 AddHost composes: this machine's model runtimes
// at their base URLs. A hosted host names a router wire_kinds member instead.
const list<string> local_host_kinds = ["ollama", "lmstudio", "lemonade", "whispercpp", "piper", "comfyui", "swarmui", "docker-model-runner", "foundry-local"]
// Profiles a host serves, the seed of an open catalogue: a later profile is a
// member here or <owner>/<name>@<n>. HostEntry.profiles records them, and
// chat@1 picks only a host serving chat (router@1 Pick profile).
const list<string> host_profiles = ["chat", "embed", "transcription", "speech", "image", "live"]
// The declared_by words of a host: operator for one added through operator@1,
// the product's name for a host that product's own record declared (its
// environment variable, server configuration file, settings store or status
// command, never a port scan), and default for a runtime at the router's
// built-in address because the product records none.
const list<string> host_declarers = ["operator", "ollama", "lmstudio", "docker-model-runner", "foundry-local", "default"]
// The consumer contract name the service gives abstraction.credentials
// applier@1 for every outgoing request to a hosted host.
const list<string> credential_consumers = ["abstraction.inference/chat@1", "abstraction.inference/embed@1", "abstraction.inference/transcription@1", "abstraction.inference/speech@1", "abstraction.inference/live@1"]

enum Role {
 1: system
 2: user
 3: assistant
 4: tool
}(unknown="refuse",reader="act")
enum PartKind {
 1: text
 2: image
 3: tool_call
 4: tool_result
}(unknown="refuse",reader="act")
struct Part {
 1: required PartKind kind
 2: optional string text(omit="zero")
 3: optional string digest(omit="zero")
 4: optional string media_type(omit="zero")
 5: optional string call_id(omit="zero")
 6: optional string name(omit="zero")
 7: optional string arguments(omit="zero")
}(unknown_fields="refuse",doc="One piece of a message. text: text. image: only in a user message; digest is a canonical lowercase sha256 digest, media_type is one of image/png, image/jpeg, image/gif or image/webp, and the bytes are resolved under the original caller's rights and verified before upstream. No image bytes travel in a request. tool_call: call_id, name and arguments, a JSON text. tool_result: call_id and text. Fields a kind does not name are empty. In a delta, a part extends the reply part at its index: text and arguments append, and call_id, name, digest and media_type are set by the first delta of that index.")
struct Message {
 1: required Role role
 2: required list<Part> parts
}(unknown_fields="refuse",doc="parts is 1..64 parts in a request. A reply message has role assistant.")
struct Tool {
 1: required string name
 2: required string description
 3: required string parameters
}(unknown_fields="refuse",doc="A function the model may call. name is 1..64 bytes of A-Z a-z 0-9 _ -; parameters is a JSON Schema object as JSON text.")
struct Temperature {
 1: required i64 milli
}(unknown_fields="refuse",doc="Sampling temperature in thousandths, 0..2000.")
struct Options {
 1: optional i64 max_output(omit="zero")
 2: optional Temperature temperature(omit="absent")
 3: optional list<string> stop(omit="zero")
 4: optional string json_schema(omit="zero")
}(unknown_fields="refuse",doc="max_output bounds output tokens (0 is the host's default). temperature, when absent, is the host's default. stop is 0..8 sequences. json_schema is a JSON Schema object as JSON text the reply must satisfy; a host without abstraction.inference/json-schema@1 refuses it.")
struct Request {
 1: required string model
 2: required list<Message> messages
 3: optional list<Tool> tools(omit="zero")
 4: optional Options options(omit="absent")
 5: optional map<string,string> extensions(omit="zero")
 6: optional list<string> required_extensions(omit="zero")
 7: required list<RequestGuarantee> guarantees
 8: optional string credential(omit="zero")
}(document="true",unknown_fields="refuse",doc="model is a model family or any alias a host lists it under, as abstraction.router/router@1 folds names. messages is 1..512. tools is 0..128. extensions holds vendor-prefixed keys <owner>/<name> with string values; a host ignores a key it does not know unless required_extensions names it, and then refuses unsupported_feature with reason extension:<key>. guarantees holds RequestGuarantee members. credential names an abstraction.credentials entry of the caller's account that a hosted host may spend under. The request carries no base URL, key, header or host name, and the encoded request fits one 1 MiB control frame.")
struct Usage {
 1: required i64 input
 2: required i64 output
 3: required i64 cached
}(unknown_fields="refuse",doc="Token counts the host reported: input, output, and cached input tokens counted within input. Zero where the host reported none.")
struct Cost {
 1: required i64 micros
 2: required string currency
}(unknown_fields="refuse",doc="Spend the host reported for this reply, in millionths of currency, an ISO 4217 code or the host's own credit unit. Absent when the host reports no cost.")
enum ReplyOutcome {
 1: completed
 2: refused
 3: not_permitted
 4: budget_exceeded
 5: unsupported_feature
 6: no_host
 7: cancelled
 8: unavailable
 9: invalid
 10: forbidden
 11: exhausted
}(unknown="refuse",reader="act")
enum StopReason {
 1: no_stop
 2: end
 3: max_output
 4: stop_sequence
 5: tool_calls
 6: content_filter
 7: other
}(unknown="refuse",reader="act")
struct Reply {
 1: required ReplyOutcome outcome
 2: required string reason
 3: required Message message
 4: required StopReason stop_reason
 5: required Usage usage
 6: required string host
 7: required string model
 8: optional Cost cost(omit="absent")
}(unknown_fields="refuse",doc="The result of one operation. completed carries the assistant message and stop_reason other than no_stop; every other outcome carries no_stop. refused means the host answered and refused the request (reason upstream:<status>). not_permitted carries reason rights:<word> for the complete decision on host:<name>, or credential:<outcome>:<name> for an applier refusal. budget_exceeded carries ceiling:<unit>:<credential>. unsupported_feature carries feature:<name> or extension:<key>. no_host carries router:<verdict>. cancelled carries cancel, idle or caller. unavailable carries rights:unavailable, credential:unavailable:<name> or upstream:<detail>; parts already delivered stay delivered. host and model name what served it, and are empty when nothing did. In the terminal delta message.parts is empty; the parts arrived in the deltas before it.")
enum DeltaKind {
 1: part
 2: usage
 3: end
 4: segment
 5: audio
 6: transcript
 7: image_progress
 8: image_result
}(unknown="refuse",reader="act")
struct Delta {
 1: required i64 sequence
 2: required DeltaKind kind
 3: optional i64 index(omit="zero")
 4: optional Part part(omit="absent")
 5: optional Usage usage(omit="absent")
 6: optional Reply end(omit="absent")
 7: optional TranscriptSegment segment(omit="absent")
 8: optional TranscriptionReply transcription_end(omit="absent")
 9: optional AudioChunk audio(omit="absent")
 10: optional SpeechReply speech_end(omit="absent")
 11: optional LiveTranscript transcript(omit="absent")
 12: optional LiveReply live_end(omit="absent")
 13: optional ImageProgress image_progress(omit="absent")
 14: optional ImageResult image_result(omit="absent")
 15: optional ImageReply image_end(omit="absent")
}(unknown_fields="refuse",doc="sequence numbers an operation's deltas from 0 without gaps. chat operations use part, usage and end; transcription uses segment and transcription_end; speech uses audio chunks and speech_end; live uses transcript, audio and live_end; image uses image_progress, image_result and image_end. Text and audio bytes are bounded so every delta fits one service frame.")
enum StartOutcome {
 1: accepted
 2: not_permitted
 3: budget_exceeded
 4: unsupported_feature
 5: no_host
 6: unavailable
 7: invalid
 8: forbidden
 9: exhausted
}(unknown="refuse",reader="act")
struct Admission {
 1: required StartOutcome outcome
 2: required string reason
 3: required string operation
 4: required string host
 5: required string model
 6: required i64 retention_ms
 7: required i64 idle_ms
 8: required i64 retained_deltas
}(unknown_fields="refuse",doc="accepted carries an operation id of 1..128 bytes, the chosen host and model, and the provider's bounds: an ended operation stays observable for retention_ms; an operation with no Observe in flight or received for idle_ms is cancelled with reason idle, and its upstream request closed; the newest retained_deltas deltas stay readable. Every refusal carries the same reason words as Reply, an empty operation, host and model, and zero bounds. exhausted means the caller's concurrent operations are at the provider's bound.")
enum PageOutcome {
 1: page
 2: gap
 3: unknown
 4: invalid
 5: forbidden
 6: unavailable
}(unknown="refuse",reader="act")
struct DeltaPage {
 1: required PageOutcome outcome
 2: required list<Delta> deltas
 3: required i64 next
 4: required bool at_end
}(unknown_fields="refuse",doc="page carries 0..max_deltas deltas from sequence cursor in order, within max_bytes of their compact JSON encoding except that a first delta larger than max_bytes is returned alone. next is the sequence after the last delta returned. at_end is true when the page holds the end delta or cursor is already past it. gap means cursor is older than the retained deltas: it carries no deltas and next set to the oldest retained sequence, from which a caller that accepts the loss continues. unknown means no operation of that id is visible to the caller: never started by it, retired after retention, or from an earlier provider epoch. Other refusals carry no deltas, next equal to cursor and at_end false.")
enum CancelOutcome {
 1: cancelled
 2: ended
 3: unknown
 4: invalid
 5: forbidden
 6: unavailable
}(unknown="refuse",reader="act")
struct Cancellation {
 1: required CancelOutcome outcome
 2: optional ReplyOutcome reply_outcome(omit="absent")
}(unknown_fields="refuse",doc="cancelled means this call stopped the operation and closed its upstream request; the end delta reads cancelled with reason cancel. ended means the operation had already ended, and reply_outcome says how. Both carry reply_outcome; other outcomes carry none.")
service Chat {
 Admission Start(1:Request request)(doc="Admit one model call for the bound caller and begin it. In order: argument shape (invalid), guarantees and host selection through the router (no_host), host features and required extensions (unsupported_feature), the rights decision for abstraction.inference/complete on host:<name> (not_permitted, or unavailable when the decision point cannot answer), the credential's ceiling (budget_exceeded), the caller's concurrent operations (exhausted), then for a hosted host the credentials applier (not_permitted or unavailable). Nothing leaves the service before every check passes. Deltas are observed with Observe.")
 DeltaPage Observe(1:string operation,2:i64 cursor,3:i64 max_deltas,4:i64 max_bytes,5:i64 wait_ms)(doc="Read a bounded page of an operation's deltas from cursor. max_deltas is 1..256, max_bytes 1..65536 and wait_ms 0..30000. When no delta at cursor exists yet and the operation has not ended, wait up to wait_ms for one, then return what is there. An Observe in flight keeps the operation from idling; a caller that disconnects mid-wait stops the wait. Reconnecting with the last next resumes without loss inside retained_deltas.")
 Cancellation Cancel(1:string operation)(doc="Stop an operation the bound caller started and close its upstream request. Reports whether this call cancelled it or it had already ended.")
}(wire_name="abstraction.inference/chat@1",doc="Model calls performed by the receiving runtime for the bound caller. Operations are visible only to the account and program that started them. Every request to a hosted host carries headers from abstraction.credentials applier@1 applied for the bound caller, sent once and kept out of every reply, delta, record and log. Caller exit cancels: an operation nobody observes for idle_ms is closed, because a stream nobody reads is spend. A decision point that cannot answer reads unavailable, never permission.")

struct EmbedRequest {
 1: required string model
 2: required list<string> inputs
 3: optional i64 dimensions(omit="zero")
 4: required list<RequestGuarantee> guarantees
 5: optional string credential(omit="zero")
 6: optional map<string,string> extensions(omit="zero")
}(unknown_fields="refuse",doc="model is a model family or alias as in Request. inputs is 1..64 texts of 1..32768 bytes each. dimensions, when not zero, is 1..4096 and asks the host for vectors of that length; a reply of another length reads unavailable with reason upstream:dimensions. guarantees and credential select hosts and spend exactly as in Request. extensions holds vendor-prefixed keys <owner>/<name> with string values that a host ignores unless it knows them. The encoded request fits one 1 MiB control frame.")
enum EmbedOutcome {
 1: completed
 2: refused
 3: not_permitted
 4: budget_exceeded
 5: unsupported_feature
 6: no_host
 7: unavailable
 8: invalid
 9: forbidden
}(unknown="refuse",reader="act")
struct Embeddings {
 1: required EmbedOutcome outcome
 2: required string reason
 3: required list<string> vectors
 4: required i64 dimensions
 5: required Usage usage
 6: required string host
 7: required string model
 8: optional Cost cost(omit="absent")
}(unknown_fields="refuse",doc="completed carries one vector per input, in input order, each the standard base64 of dimensions little-endian IEEE 754 float32 values, and dimensions of at least 1. The vectors of one reply hold at most 131072 values together; a larger upstream reply reads unavailable with reason upstream:too_large, and a caller embedding long vectors sends fewer inputs. usage.input is the input tokens the host counted. Every other outcome carries no vectors, zero dimensions, zero usage, and the reason words of Reply; unsupported_feature carries wire:<kind> for a host whose wire has no embeddings call. host and model name what served the call, and are empty when nothing did.")
service Embedder {
 Embeddings Embed(1:EmbedRequest request)(doc="Embed texts for the bound caller in one bounded call. Admission follows Chat.Start's order for profile embed, without its feature and capacity steps: argument shape (invalid), guarantees and host selection (no_host), the host's wire (unsupported_feature), the rights decision for abstraction.inference/complete on host:<name> (not_permitted, or unavailable), the credential's ceiling (budget_exceeded), then for a hosted host the credentials applier with consumer abstraction.inference/embed@1. Nothing leaves the service before every check passes. The call returns when the host replies.")
}(wire_name="abstraction.inference/embed@1",doc="Text embeddings computed by a host the receiving runtime picks, for the bound caller. Every request to a hosted host carries headers from abstraction.credentials applier@1 applied once for the bound caller and kept out of every reply, record and log. A decision point that cannot answer reads unavailable, never permission.")

enum TranscriptUnitKind {
 1: segment
 2: word
}(unknown="refuse",reader="act")
enum TimestampMode {
 1: none
 2: segment
 3: word
 4: segment_and_word
}(unknown="refuse",reader="act")
struct TranscriptionRequest {
 1: required string model
 2: required string audio_digest
 3: required string media_type
 4: optional string language(omit="zero")
 5: required TimestampMode timestamps
 6: required list<RequestGuarantee> guarantees
 7: optional string credential(omit="zero")
 8: optional map<string,string> extensions(omit="zero")
}(unknown_fields="refuse",doc="A prerecorded transcription input. audio_digest is a canonical lowercase sha256 reference read through storage under the original caller's authority; bytes never cross this request. media_type is audio/wav, audio/mpeg, audio/mp4, audio/webm, audio/ogg or audio/flac. language is empty for detection or a 1..35 byte BCP-47-like tag. timestamps selects no units, segments, words or both; segment has no additional upstream latency and word may cost more. guarantees, credential and extensions have Request's meanings. The encoded request fits one 1 MiB control frame.")
struct TranscriptSegment {
 1: required i64 index
 2: required TranscriptUnitKind kind
 3: required i64 start_ms
 4: required i64 end_ms
 5: required string text
}(unknown_fields="refuse",doc="One ordered transcript unit. index starts at zero separately for segment and word units. start_ms and end_ms are within the input duration, start_ms <= end_ms, and text is at most 32768 UTF-8 bytes so its delta fits a bounded page and service frame.")
struct TranscriptionReply {
 1: required ReplyOutcome outcome
 2: required string reason
 3: required string language
 4: required i64 duration_ms
 5: required Usage usage
 6: required string host
 7: required string model
 8: optional Cost cost(omit="absent")
}(unknown_fields="refuse",doc="The terminal result of one transcription operation. completed carries the detected or requested language and input duration in milliseconds. Other outcomes carry the common Reply outcome and reason words. unavailable reports malformed or inconsistent upstream results. host and model name what served the call, and are empty when nothing did.")
service Transcription {
 Admission Start(1:TranscriptionRequest request)(doc="Admit one prerecorded transcription for the bound caller. Admission follows chat order for profile transcription: arguments, host and wire/format support, complete rights, authorized bounded content resolution and verification, ceiling, capacity, then the profile-scoped hosted credential. Nothing leaves the service before all checks pass.")
 DeltaPage Observe(1:string operation,2:i64 cursor,3:i64 max_deltas,4:i64 max_bytes,5:i64 wait_ms)(doc="Read retained segment and terminal deltas with the shared operation cursor, page, reconnect, wait and idle semantics.")
 Cancellation Cancel(1:string operation)(doc="Stop a transcription operation and close its upstream request, using the shared operation cancellation outcomes.")
}(wire_name="abstraction.inference/transcription@1",doc="Prerecorded speech-to-text over an authorized content digest. Operations use the shared retained-delta lifecycle and are visible only through this profile to the account and program that started them.")

enum SpeechFormat {
 1: mp3
 2: opus
 3: aac
 4: flac
 5: wav
 6: pcm
}(unknown="refuse",reader="act")
struct SpeechRequest {
 1: required string model
 2: required string voice
 3: required string text
 4: required SpeechFormat format
 5: required list<RequestGuarantee> guarantees
 6: optional string credential(omit="zero")
 7: optional map<string,string> extensions(omit="zero")
}(unknown_fields="refuse",doc="One text-to-speech request. voice is a backend voice name or id of 1..256 safe URL-component bytes. text is 1..4096 Unicode scalar values and at most 16384 UTF-8 bytes. format is a codec the selected host explicitly supports. guarantees, credential and extensions have Request's meanings. No URL, path, key or header crosses this request.")
struct AudioChunk {
 1: required i64 index
 2: required binary data
}(unknown_fields="refuse",doc="One ordered speech output chunk. index starts at zero and data is 1..65536 bytes. Concatenating chunks byte-for-byte yields the content named by SpeechReply.delivery.digest.")
struct Delivery {
 1: required string digest
 2: required string media_type
 3: required i64 size
 4: required string location
}(unknown_fields="refuse",doc="A typed result reference. Completed local work uses location local and a canonical sha256 digest readable through the caller's authorized content reader. Delegated work uses the remote trust domain as location; its digest is not claimed readable in the local store. Refusals carry empty strings and size zero.")
struct SpeechReply {
 1: required ReplyOutcome outcome
 2: required string reason
 3: required Delivery delivery
 4: required i64 characters
 5: required Usage usage
 6: required string host
 7: required string model
 8: optional Cost cost(omit="absent")
}(unknown_fields="refuse",doc="The terminal result of one speech operation. completed carries the exact concatenated audio as a typed delivery and the Unicode scalar count charged. Other outcomes carry an empty delivery. host and model name what served the call.")
service Speech {
 Admission Start(1:SpeechRequest request)(doc="Admit text-to-speech for the bound caller. Admission follows chat order for profile speech: arguments, host and wire/format support, complete rights, authorized output-write preflight, ceiling, capacity, then the profile-scoped hosted credential. Nothing leaves the service before every check passes.")
 DeltaPage Observe(1:string operation,2:i64 cursor,3:i64 max_deltas,4:i64 max_bytes,5:i64 wait_ms)(doc="Read retained audio and terminal deltas with the shared operation cursor, page, reconnect, wait and idle semantics.")
 Cancellation Cancel(1:string operation)(doc="Stop a speech operation and close its upstream request, using the shared operation cancellation outcomes.")
}(wire_name="abstraction.inference/speech@1",doc="Streaming text-to-speech with bounded audio deltas and a final storage reference. Operations are visible only through this profile to the account and program that started them.")

enum LiveFormat {
 1: pcm16_24000
}(unknown="refuse",reader="act")
struct LiveRequest {
 1: required string model
 2: required string voice
 3: required LiveFormat format
 4: required list<RequestGuarantee> guarantees
 5: optional string credential(omit="zero")
 6: optional map<string,string> extensions(omit="zero")
}(unknown_fields="refuse",doc="One live audio response session. pcm16_24000 is mono signed 16-bit little-endian PCM at 24000 Hz for input and output. Voice is 1..256 safe URL-component bytes. Guarantees and credential have Request meanings. Start creates one session; Commit finishes its input and requests one final model response.")
enum LiveInputOutcome {
 1: accepted
 2: duplicate
 3: out_of_order
 4: closed
 5: invalid
 6: unknown
 7: forbidden
 8: unavailable
 9: exhausted
}(unknown="refuse",reader="act")
struct LiveInputResult {
 1: required LiveInputOutcome outcome
 2: required i64 next_sequence
}(unknown_fields="refuse",doc="next_sequence is the next Append sequence expected, starting at zero. A retry of the most recently acknowledged identical frame is duplicate and sends nothing upstream. Other repeated or skipped sequences are out_of_order. After Commit no Append is accepted. An uncertain upstream write ends the session as unavailable; the service never retries it.")
struct LiveTranscript {
 1: required string text
 2: required bool is_final
}(unknown_fields="refuse",doc="One bounded output transcript fragment, at most 16384 UTF-8 bytes. Fragments append in delta order. A final marker may have empty text. It marks transcript completion, not session completion.")
struct LiveReply {
 1: required ReplyOutcome outcome
 2: required string reason
 3: required Delivery delivery
 4: required Usage usage
 5: required i64 input_audio_bytes
 6: required i64 output_audio_bytes
 7: required string host
 8: required string model
}(unknown_fields="refuse",doc="Final live-session outcome. Completed output audio is committed through the original caller's authorized content writer; delivery names those exact bytes. Failed sessions carry empty delivery. Input and output byte counts describe PCM processed, and usage carries upstream token counts.")
service Live {
 Admission Start(1:LiveRequest request)(doc="Admit a live session after host support, complete permission, output-write preflight, budget, shared capacity and credential checks. The service owns the upstream session. Observe reports setup failures after admission.")
 LiveInputResult Append(1:string operation,2:i64 sequence,3:binary audio)(doc="Append 1..65536 even-sized PCM bytes at the expected sequence. Bounded input and idle limits apply. Concurrent appends are serialized; only confirmed sends advance next_sequence.")
 DeltaPage Observe(1:string operation,2:i64 cursor,3:i64 max_deltas,4:i64 max_bytes,5:i64 wait_ms)(doc="Read retained transcript, audio and terminal deltas using shared cursor and wait semantics.")
 LiveInputResult Commit(1:string operation)(doc="Close input and request the final response. A repeated Commit is duplicate. Observe continues until terminal delivery or failure; Commit does not wait for synthesis to finish.")
 Cancellation Cancel(1:string operation)(doc="Immediately stop the live session and its upstream connection. Uses shared operation cancellation and ownership semantics.")
}(wire_name="abstraction.inference/live@1",doc="Live audio through bounded native IPC calls. One session produces one final response. A new response starts a new session. Credentials and vendor WebSockets stay in the service.")

enum ImageMode {
 1: generate
 2: edit
}(unknown="refuse",reader="act")
struct ImageRequest {
 1: required string model
 2: required ImageMode mode
 3: required string prompt
 4: required string size
 5: required i64 count
 6: optional string image_digest(omit="zero")
 7: optional string image_media_type(omit="zero")
 8: optional string mask_digest(omit="zero")
 9: optional string mask_media_type(omit="zero")
 10: required list<RequestGuarantee> guarantees
 11: optional string credential(omit="zero")
 12: optional list<string> required_extensions(omit="zero")
 13: optional map<string,string> extensions(omit="zero")
}(unknown_fields="refuse",doc="One image generation or edit request. prompt is 1..32768 UTF-8 bytes. size is auto or WIDTHxHEIGHT with positive decimal dimensions. count is 1..10. Generate carries no input digests. Edit carries one canonical image SHA-256 digest and media type and may carry one canonical mask digest and media type. Input media types are PNG, JPEG or WebP; masks are PNG. Digests are authorized and resolved only by the selected runtime. extensions holds declared vendor workflow or template controls; a named required extension absent from the selected adapter is refused before paid work. No URL, file path, key, header, inline image or workflow crosses this request.")
struct ImageProgress {
 1: optional i64 percent(omit="zero")
 2: required string message
}(unknown_fields="refuse",doc="One bounded progress update. percent, when nonzero, is 1..100; zero means the backend reported no numeric progress. message is at most 4096 UTF-8 bytes and contains no submitted content or credential material.")
struct ImageResult {
 1: required i64 index
 2: required Delivery delivery
}(unknown_fields="refuse",doc="One completed image in request order. index is 0..count-1. delivery identifies an authorized PNG, JPEG or WebP result and the runtime that owns it.")
struct ImageReply {
 1: required ReplyOutcome outcome
 2: required string reason
 3: required list<Delivery> deliveries
 4: required i64 images
 5: required Usage usage
 6: required string host
 7: required string model
 8: optional Cost cost(omit="absent")
}(unknown_fields="refuse",doc="The terminal result of one image operation. Completed carries exactly count ordered deliveries equal to the streamed image_result deltas and images is the charged output count. Other outcomes carry no deliveries. host and model name what served the call.")
service Image {
 Admission Start(1:ImageRequest request)(doc="Admit image generation or edit for the bound caller. Admission checks arguments, explicit host mode/size/count support, complete rights, authorized input and mask resolution, output-write preflight, ceiling, shared capacity and the profile-scoped credential before paid work.")
 DeltaPage Observe(1:string operation,2:i64 cursor,3:i64 max_deltas,4:i64 max_bytes,5:i64 wait_ms)(doc="Read retained progress, result and terminal deltas with the shared operation cursor, page, reconnect, wait and idle semantics.")
 Cancellation Cancel(1:string operation)(doc="Stop image work, including queue polling, and cancel the upstream job where its wire supports cancellation.")
}(wire_name="abstraction.inference/image@1",doc="Image generation and edit with authorized digest inputs, bounded progress and typed result deliveries. Operations are visible only through this profile to the account and program that started them.")

struct CeilingLimit {
 1: optional i64 tokens_per_day(omit="zero")
 2: optional i64 micros_per_day(omit="zero")
 3: optional i64 requests_per_day(omit="zero")
 4: optional i64 images_per_day(omit="zero")
 5: optional i64 audio_seconds_per_day(omit="zero")
 6: optional i64 characters_per_day(omit="zero")
}(unknown_fields="refuse",doc="A credential's daily limits per UTC day: tokens (input plus output), spend in currency millionths, requests, images, audio seconds and characters. Zero or absent means no limit in that unit. The provider enforces every configured unit at admission and records the applicable units when each profile ends.")
// The request extension a runtime delegating to a remote runtime sets to the
// program that asked it. The remote records it as a claim attributed to the
// caller its certificate maps to, and never decides on it.
const list<string> claim_extensions = ["openabstractions/claimed-program"]
struct HostEntry {
 1: required string name
 2: required bool hosted
 3: required string kind
 4: required string base
 5: optional string credential(omit="zero")
 6: optional CeilingLimit ceiling(omit="absent")
 7: optional list<string> profiles(omit="zero")
 8: optional string declared_by(omit="zero")
}(unknown_fields="refuse",doc="One host the runtime reaches. A remote runtime is an abstraction.facade/registry@1 declaration; router@1 Hosts lists its hosts as <name>/<host>. profiles holds 1..16 distinct host_profiles members or <owner>/<name>@<n>; empty in AddHost selects the wire's default: openai-compatible and every local kind serve chat, embed, transcription, speech and image, and anthropic-messages and any other wire serve chat. declared_by is asserted by the runtime, operator for a host added through AddHost, and AddHost refuses a non-empty value as invalid. name is 1..64 bytes of a-z 0-9 _ - and unique. A local host (hosted false) names a local_host_kinds member as both name and kind, and the base URL of that runtime on this machine, and carries no credential or ceiling. A hosted host names a wire kind (router wire_kinds or <owner>/<name>@<n>), its https or loopback http API root, the abstraction.credentials name the service applies to it, and optionally that credential's ceiling. base carries no user information, query or fragment.")
struct Spend {
 1: required string day
 2: required i64 tokens
 3: required i64 micros
 4: optional i64 requests(omit="zero")
 5: optional i64 audio_seconds(omit="zero")
 6: optional i64 characters(omit="zero")
 7: optional i64 images(omit="zero")
}(unknown_fields="refuse",doc="What a credential has spent on UTC day (YYYY-MM-DD), counted from provider replies: tokens, currency millionths, requests, generated images, whole audio seconds and characters.")
struct HostState {
 1: required HostEntry entry
 2: required bool up
 3: required string why
 4: optional Spend spend(omit="absent")
}(unknown_fields="refuse",doc="A configured host and the router's latest reading of it: up, or why not. spend is present for a hosted host that names a credential.")
enum ListOutcome {
 1: page
 2: invalid
 3: forbidden
 4: unavailable
}(unknown="refuse",reader="act")
struct HostList {
 1: required ListOutcome outcome
 2: required string revision
 3: required list<HostState> hosts
}(unknown_fields="refuse",doc="page carries every configured host, at most 64, and the configuration revision AddHost and RemoveHost take. invalid is reserved for a request this provider cannot interpret. Refusals carry an empty revision and no hosts.")
enum EditOutcome {
 1: applied
 2: conflict
 3: unknown
 4: invalid
 5: no_secure_store
 6: forbidden
 7: unavailable
}(unknown="refuse",reader="act")
struct HostChange {
 1: required EditOutcome outcome
 2: required string revision
 3: required string reason
}(unknown_fields="refuse",doc="applied carries the new configuration revision; the runtime reads the hosts again at once. conflict means expected_revision is not current, or AddHost named an existing host, and carries the current revision. unknown means RemoveHost named no host. invalid carries the field in reason. Other outcomes carry an empty revision. no_secure_store is not used by host edits.")
enum KeyState {
 1: active
 2: revoked
 3: lost
}(unknown="refuse",reader="act")
struct LocalKey {
 1: required string program
 2: required string name
 3: optional string credential(omit="zero")
 4: required i64 issued_unix_ms
 5: required string issued_by
 6: required KeyState state
}(unknown_fields="refuse",doc="A local key minted for one program. program is the normalized absolute executable path the window requires of the bound peer presenting the key. name is the abstraction.credentials record holding it as kind openabstractions/local-key@1. credential, when present, is the hosted credential name the window's requests may spend under; without it the window's requests are local-only. issued_unix_ms is when it was minted, in milliseconds since the Unix epoch. issued_by is the operator program that issued it. lost means the platform store no longer holds the key. Metadata only; no field derives from the key.")
struct KeyList {
 1: required ListOutcome outcome
 2: required list<LocalKey> keys
}(unknown_fields="refuse",doc="page carries every retained local key of the receiving account, at most 256, revoked ones included. Refusals carry no keys.")
struct KeyIssued {
 1: required EditOutcome outcome
 2: required string reason
 3: optional string key(omit="zero")
 4: optional LocalKey record(omit="absent")
}(unknown_fields="refuse",doc="applied carries the key, once, and its record. The key is the only secret operator@1 ever returns: the runtime minted it, it authorizes nothing by itself, and no later call reads it back. The operator gives it to the program, for example as that program's API key setting. conflict means the program holds an active key; revoke it first. invalid carries the field in reason. no_secure_store means the runtime has no platform store to hold it. Other outcomes carry no key and no record.")
struct KeyRevoked {
 1: required EditOutcome outcome
}(unknown_fields="refuse",doc="applied destroys the key in the platform store; the window refuses it from the next request. unknown means the program holds no active key.")
enum AuditRoute {
 1: native
 2: window
 3: remote
}(unknown="refuse",reader="act")
struct AuditEntry {
 1: required i64 sequence
 2: required i64 unix_ms
 3: required AuditRoute route
 4: required string rung
 5: required string program
 6: required string operation
 7: required string host
 8: required string model
 9: required string credential
 10: required string outcome
 11: required string reason
 12: required i64 tokens_in
 13: required i64 tokens_out
 14: required i64 wall_ms
 15: required string ceiling
 16: optional string domain(omit="zero")
 17: optional string claim(omit="zero")
 18: optional string profile(omit="zero")
 19: optional i64 audio_seconds(omit="zero")
 20: optional i64 characters(omit="zero")
 21: optional i64 images(omit="zero")
}(unknown_fields="refuse",doc="profile is the host profile of the call; empty is chat. audio_seconds is the rounded-up input duration counted for transcription, characters is text sent for speech, and images is the generated image count. domain names the other trust domain of a decision: on the delegating runtime the remote host it went to, and on the remote runtime the caller its client certificate maps to. claim, on the remote runtime, is the program the delegating runtime says asked, attributed to domain; it decides nothing. route remote is a call that arrived over the remote transport, whose rung names that transport. One decision in journal order, at unix_ms milliseconds since the Unix epoch: an ended operation, an admission refusal, or a window refusal before admission. Entries carry names and counts, never headers, credentials or content.")
enum AuditOutcome {
 1: page
 2: gap
 3: invalid
 4: forbidden
 5: unavailable
}(unknown="refuse",reader="act")
struct AuditPage {
 1: required AuditOutcome outcome
 2: required list<AuditEntry> entries
 3: required i64 next
 4: required bool at_end
}(unknown_fields="refuse",doc="page carries at most max_entries entries from sequence cursor in order. The runtime retains its newest entries across restarts; a cursor older than the oldest retained entry reads gap with next at the oldest retained sequence and no entries. at_end is true when no entry after the page exists yet. Other refusals carry no entries, next equal to cursor and at_end false.")
struct GatewayState {
 1: required ListOutcome outcome
 2: required string revision
 3: required bool open
 4: required string address
 5: required bool listening
 6: required string listening_address
 7: required string why
}(unknown_fields="refuse",doc="page carries the gateway window setting kept in the runtime state at revision, and the window in this runtime now. open and address are the setting; it is off by default and address is empty until one is set. listening is true when the window accepts connections at listening_address. A window opened for this process by serve runtime --gateway reads listening with the setting unchanged. why names the failure when open is set and the window is not listening, as listen:<detail>. Refusals carry an empty revision, addresses and why, and open and listening false.")
struct GatewayChange {
 1: required EditOutcome outcome
 2: required string revision
 3: required string reason
}(unknown_fields="refuse",doc="applied carries the new setting revision; the runtime has opened or closed the window before replying. conflict means expected_revision is not current and carries the current revision. invalid carries the field in reason: address when it is not 127.0.0.1:<port>. unavailable carries listen:<detail> in reason when the setting was written and the window could not listen. Other outcomes carry an empty revision.")
service Operator {
 HostList Hosts()(doc="Read the configured hosts, their router state and each hosted credential's spend today. Gated by abstraction.inference/host.manage on resource account.")
 HostChange AddHost(1:string expected_revision,2:HostEntry host)(doc="Conditionally add one host. Gated by host.manage. For the bound operator program and the runtime's other operator programs it also writes the permit rule abstraction.inference/complete on host:<name>, and for a hosted host with a credential it writes the runtime's own permit rule abstraction.credentials/apply on credential:<name>, which the router's listing reads need. Existing rules are left as they are.")
 HostChange RemoveHost(1:string expected_revision,2:string name)(doc="Conditionally remove one host; operations already admitted run to their end. Gated by host.manage. Rules are left as they are.")
 KeyList Keys()(doc="Read the local keys of the receiving account. Gated by abstraction.inference/key.issue on resource account.")
 KeyIssued IssueKey(1:string program,2:string credential)(doc="Mint a local key for program, an absolute executable path, and hold it in the platform store. credential, when not empty, names the hosted credential the window's requests for that program may spend under. Gated by key.issue. The key grants nothing: the window still binds the peer, requires that program, and decides abstraction.inference/complete and credentials apply for it.")
 KeyRevoked RevokeKey(1:string program)(doc="Destroy the active local key of program. Gated by key.issue.")
 AuditPage Audit(1:i64 cursor,2:i64 max_entries)(doc="Read retained inference decisions from both routes. Gated by abstraction.inference/audit.read on resource account. cursor 0 starts at the oldest retained entry; max_entries is 1..256.")
 GatewayState Gateway()(doc="Read the gateway window setting and whether the window listens now. Gated by abstraction.inference/host.manage on resource account.")
 GatewayChange SetGateway(1:string expected_revision,2:bool open,3:string address)(doc="Conditionally write the gateway window setting to the runtime state and apply it to the running runtime: open listens on address, and closed stops the listener and every window connection. address is 127.0.0.1:<port>; empty keeps the recorded address when opening, and 127.0.0.1:8793 when none is recorded. The setting survives a restart. Gated by host.manage.")
}(wire_name="abstraction.inference/operator@1",doc="Administer the runtime's inference hosts, the gateway window and its local keys, and read the inference audit. Each call is a rights decision for the bound operator subject; same-account identity alone grants nothing. A decision point that cannot answer reads unavailable.")
