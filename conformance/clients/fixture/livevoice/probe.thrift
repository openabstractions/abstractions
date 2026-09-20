namespace * oa.test.livevoice

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
 10: bad_binary(stage="structure")
 11: trailing_bytes(stage="document")
}

struct Frame {
 1: required i64 sequence
 2: required binary audio
}(document="true",unknown_fields="refuse",doc="Measurement-only 200 ms audio frame; not a public inference profile.")
struct Sample {
 1: required i64 sequence
 2: required binary audio
 3: required i64 wait_ns
}(unknown_fields="refuse",doc="Measured backend or long-poll wait to subtract from the local exchange round trip.")
service VoiceProbe {
 Sample Append(1:Frame frame)
 Sample Observe(1:i64 sequence)
}(wire_name="oa.test/livevoice-probe@1",doc="Isolated latency instrument using generated codecs and the production identity-bound transport.")
