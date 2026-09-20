//! A Rust application resolves the credentials holder and applier through the
//! facade over the native transport. It holds no holder.read rule and is no
//! designated enforcer: both calls read forbidden, and no reply carries a secret.
use abstraction_facade_credentials::{wire, CredentialsMachine};
use abstraction_facade_native::Machine;

fn main() {
    let a: Vec<String> = std::env::args().collect();
    if a.len() == 2 && a[1] == "--help" {
        println!("credentials-consumer <runtime> <account> [secret ...]");
        return;
    }
    assert!(a.len() >= 3, "usage: credentials-consumer <runtime> <account> [secret ...]");
    let machine = Machine::new(&a[1]);
    let holder = machine.resolve_credentials(vec![], abstraction_facade_native::Scope::Local).unwrap();
    let applier = machine.resolve_credentials_applier(vec![], abstraction_facade_native::Scope::Local).unwrap();
    let page = holder.list("", 64).unwrap();
    assert!(page.outcome == "forbidden" && page.records.is_empty(), "list: {:?}", page.outcome);
    let usage = wire::Use {
        subject: wire::Subject { account: a[2].clone(), program: std::env::current_exe().unwrap().display().to_string() },
        consumer: "abstraction.download/http-execution@1".into(),
        name: "hf".into(),
        target: "huggingface.co".into(),
        challenge: String::new(),
    };
    let applied = applier.apply(usage).unwrap();
    assert!(applied.outcome == "forbidden" && applied.headers.is_empty(), "apply: {:?}", applied.outcome);
    let replies = format!("{page:?}{applied:?}");
    assert!(a[3..].iter().all(|secret| !replies.contains(secret.as_str())), "a reply carries a secret");
    println!("PASS rust: list={} apply={}", page.outcome.as_str(), applied.outcome.as_str());
}
