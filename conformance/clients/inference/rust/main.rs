//! A Rust application resolves abstraction.inference/chat@1 through the facade
//! over the native transport, and calls complete() and the stream() iterator.
use abstraction_facade_inference::{wire, InferenceMachine};
use abstraction_facade_native::Machine;
use std::time::Instant;

fn request(model: &str, text: &str) -> wire::Request {
    wire::Request {
        model: model.into(),
        messages: vec![wire::Message {
            role: wire::Role::User,
            parts: vec![wire::Part {
                kind: wire::PartKind::Text,
                text: text.into(),
                digest: String::new(),
                media_type: String::new(),
                call_id: String::new(),
                name: String::new(),
                arguments: String::new(),
            }],
        }],
        tools: vec![],
        options: None,
        extensions: Default::default(),
        required_extensions: vec![],
        guarantees: vec![wire::request_guarantee::LOCAL_ONLY.to_string()],
        credential: String::new(),
    }
}

fn main() {
    let a: Vec<String> = std::env::args().collect();
    if a.len() == 2 && a[1] == "--help" {
        println!("inference-consumer <runtime> <runtime without inference> refused|served");
        return;
    }
    assert!(a.len() == 4, "usage: inference-consumer <runtime> <runtime without inference> refused|served");
    match Machine::new(&a[2]).resolve_inference(vec![], abstraction_facade_native::Scope::Local) {
        Ok(_) => panic!("a runtime without inference resolved chat@1"),
        Err(e) => assert!(format!("{e:?}").contains("Unavailable"), "absence: {e:?}"),
    }
    let chat = Machine::new(&a[1]).resolve_inference(vec![], abstraction_facade_native::Scope::Local).unwrap();
    if a[3] == "refused" {
        let reply = chat.complete(request("fixture-chat:1b", "hi")).unwrap();
        assert!(reply.outcome == wire::ReplyOutcome::NotPermitted && reply.reason == "rights:not_granted", "{reply:?}");
        println!("PASS rust refused: absence=unavailable complete={}", reply.outcome.as_str());
        return;
    }
    assert_eq!(a[3], "served");
    let reply = chat.complete(request("fixture-chat:1b", "hi")).unwrap();
    assert!(
        reply.outcome == wire::ReplyOutcome::Completed
            && reply.message.parts.len() == 1
            && reply.message.parts[0].text == "Hello from the fixture runtime"
            && reply.host == "ollama"
            && reply.usage.input == 5,
        "{reply:?}"
    );
    let started = Instant::now();
    let mut first = None;
    let mut text = String::new();
    let mut last = None;
    for delta in chat.stream(request("fixture-chat:1b", "hi")) {
        let delta = delta.unwrap();
        if delta.kind == wire::DeltaKind::Part {
            first.get_or_insert_with(|| started.elapsed().as_secs_f64() * 1000.0);
            text.push_str(&delta.part.as_ref().unwrap().text);
        }
        last = Some(delta);
    }
    let last = last.unwrap();
    assert!(text == "Hello from the fixture runtime" && last.end.unwrap().outcome == wire::ReplyOutcome::Completed);
    for delta in chat.stream(request("fixture-chat:1b", "HOLD")) {
        if delta.unwrap().kind == wire::DeltaKind::Part {
            break;
        }
    }
    let denied = chat.complete(request("denied-chat", "hi")).unwrap();
    assert!(denied.outcome == wire::ReplyOutcome::NotPermitted && denied.reason == "rights:not_granted", "{denied:?}");
    println!("PASS rust served: complete, stream, cancel, refusal");
    println!("FIRST_TOKEN_MS rust {:.2}", first.unwrap());
}
