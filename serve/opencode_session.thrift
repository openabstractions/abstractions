namespace go opencode.session
namespace javascript opencode.session

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
 3: number_spelling(stage="grammar")
 4: wrong_type(stage="grammar")
 5: depth_exceeded(stage="grammar")
 6: duplicate_key(stage="grammar")
 7: duplicate_field(stage="structure")
 8: unknown_field(stage="structure")
 9: missing_field(stage="structure")
 10: bad_enum(stage="structure")
 11: trailing_bytes(stage="document")
}

enum ProjectOutcome {
 1: pending
 2: denied
 3: expired
 4: conflict
 5: approved_pending_close
 6: receipt_recorded
 7: unavailable
}(unknown="refuse")

struct ProjectRequest {
 1: required string request_key
 2: required string folder
 3: required string session_id
 4: required string message_id
}(document="true",unknown_fields="refuse",doc="Request data from the current native OpenCode job. The session owner fixes access to recursive Modify, captures the folder FileKey, and derives caller identity from native IPC. No request field grants access.")

struct ProjectReceipt {
 1: required ProjectOutcome outcome
 2: required string request_key
 3: required string receipt
 4: required string question_id
 5: required string message
}(unknown_fields="refuse",doc="A manual approval waits for its committed completed-tool receipt and an explicit user close and fresh launch. It creates no OS grant while the current fixed-grant job runs.")

struct ProjectCompletion {
 1: required string request_key
 2: required string session_id
 3: required string message_id
 4: required string part_id
 5: required string tool_call_id
 6: required string receipt
 7: required i64 completed_at
}(unknown_fields="refuse",doc="The installed plugin acknowledges only its matching committed completed tool part, with the actual PartID, SessionID, MessageID, ToolCallID and state.time.end Unix milliseconds supplied by the paired application's post-commit part event. The native owner durably retains this acknowledgement before replying. It establishes no idle or frozen claim and never stops work, changes grants or replays a prompt.")

service SessionProject {
 ProjectReceipt RequestProject(1:ProjectRequest request)
 ProjectReceipt AcknowledgeReceipt(1:ProjectCompletion completion)
}(wire_name="openabstractions.opencode/session-project@1",doc="Private leaf hosted by the existing native CLI session owner, using generated framing and shared native identity transport. Every call requires current account/program/job PID+start proof. The owner binds the manual once answer, exact folder FileKey, session and expiry before any separate kit grant effect.")
