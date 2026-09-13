namespace * abstraction.facade

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

enum Scope { 1: any 2: local 3: remote }(unknown="refuse")
enum ResolutionStatus {
 1: resolved
 2: unavailable
 3: forbidden
 4: incompatible
 5: unmet_requirements
 6: not_ready
 7: invalid_request
}(unknown="refuse")
struct ResolveRequest {
 1: required string capability
 2: required list<string> contracts
 3: required list<string> guarantees
 4: required Scope scope
}(unknown_fields="refuse",doc="Capability and acceptable contract identities, required guarantees and permitted placement. Contains no caller identity or provider preference. Contract list must be nonempty; guarantees may be empty.")
struct ServiceReference {
 1: required string provider
 2: required string capability
 3: required string contract
 4: required list<string> guarantees
 5: required Scope scope
 6: required string transport
 7: required string endpoint
}(unknown_fields="refuse",doc="A candidate service binding, not acceptance or authority. Provider identifies a logical provider, not a PID. Scope is local or remote. Contract is an exact versioned service identity; endpoint is opaque to application code.")
struct ResolveResult {
 1: required ResolutionStatus status
 2: optional ServiceReference reference(omit="absent")
}(document="true",unknown_fields="refuse",doc="Reference is present exactly when resolved. Failure status describes the first unsatisfied resolution stage without exposing disallowed provider metadata. Service authentication and acceptance remain necessary after resolution.")
service Resolver {
 ResolveResult Resolve(1:ResolveRequest request)(doc="Find an authorized ready candidate satisfying an exact acceptable contract and all required guarantees. Does not submit work, activate an embedded provider, or transfer ownership.")
}(wire_name="abstraction.facade/resolver@1",doc="Common provider resolution vocabulary. Authorization comes from the receiving boundary, never from request fields. See RESOLUTION.md for selection and refusal semantics.")

// Platform evidence and resolver evidence are independent observations.
enum BootstrapState {
 1: unknown
 2: installed
 3: starting
 4: running
 5: unavailable
}(unknown="refuse")
struct BootstrapObservation {
 1: required BootstrapState state
 2: optional string detail(omit="absent")
}(unknown_fields="refuse",doc="Read-only platform registration or supervisor evidence. Unknown means observation is unavailable; unavailable requires actual evidence that the selected registration is absent. Installed and starting require registration or supervisor evidence. Running describes a supervisor process and does not establish capability readiness. A missing endpoint alone establishes none of these states. Detail is diagnostic text, never authority or a recovery instruction.")
struct CapabilityObservation {
 1: required ResolveRequest request
 2: optional ResolveResult result(omit="absent")
}(unknown_fields="refuse",doc="One authorized resolver observation. An absent result means unobserved because the query was not completed; it does not imply unavailable. Existing resolution statuses and disclosure rules apply unchanged. A resolved reference remains a candidate binding, not proof of successful provider calls.")
struct RuntimeObservation {
 1: required BootstrapObservation bootstrap
 2: required list<CapabilityObservation> capabilities
}(unknown_fields="refuse",doc="Client-composed point-in-time diagnostics, with no new service or activation operation. Platform evidence is explicit and may be unknown. Capability queries use one caller waiting budget and authority; observations are sequential and not an atomic snapshot. Transport or cancellation errors are reported separately by the language observation API; unanswered entries retain absent results.")

const list<string> default_runtime_contracts = [
 "abstraction.logging/sink@1",
 "abstraction.config/reader@1",
 "abstraction.job/acceptance@1",
 "abstraction.job/operations@1",
 "abstraction.config/editor@1"
]
