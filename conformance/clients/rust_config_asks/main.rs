use abstraction_facade_asks::{wire as asks, AsksMachine, Error as AsksError};
use abstraction_facade_config::{wire as config, ConfigMachine, Error as ConfigError};
use abstraction_facade_native::Machine;
use std::collections::BTreeMap;

fn settings(v: &config::UserSettings) -> config::UserSettings {
    config::UserSettings {
        nas_store: v.nas_store.clone(),
        store: v.store.clone(),
        log_sink: v.log_sink.clone(),
        log_service: v.log_service.clone(),
        off: v.off.clone(),
    }
}

fn question(request_key: &str, host: &str) -> asks::ApplicationQuestion {
    let mut slots = BTreeMap::new();
    slots.insert("host".to_string(), host.to_string());
    asks::ApplicationQuestion {
        request_key: request_key.into(),
        key: "download.reach".into(),
        slots,
    }
}

fn config_and_questions(machine: &Machine, policy_file: &str) {
    let set_policy = |mode: &str| std::fs::write(policy_file, mode).unwrap();
    let editor = machine.resolve_config_editor(vec![], abstraction_facade_native::Scope::Local).unwrap();
    let before = editor.read_user().unwrap();
    let mut values = settings(&before.values);
    values.off.insert("rust-edit".into(), "applied".into());
    let applied = editor.replace_user(&before.revision, values).unwrap();
    assert_eq!(applied.outcome, "applied");
    assert_eq!(applied.snapshot.values.off["rust-edit"], "applied");
    assert_eq!(editor.read_user().unwrap().revision, applied.snapshot.revision);
    let stale = editor.replace_user(&before.revision, settings(&before.values)).unwrap();
    assert_eq!((stale.outcome.as_str(), stale.snapshot.revision.as_str()), ("conflict", applied.snapshot.revision.as_str()));
    assert!(matches!(editor.replace_user("", settings(&before.values)), Err(ConfigError::Invalid(_))));
    for mode in ["forbidden", "unavailable"] {
        set_policy(mode);
        let refused = editor.replace_user(&applied.snapshot.revision, settings(&before.values)).unwrap();
        assert_eq!(refused.outcome, mode);
        assert!(refused.snapshot.revision.is_empty() && refused.snapshot.values.off.is_empty());
        assert_eq!(editor.read_user().unwrap().revision, applied.snapshot.revision, "{mode} edit changed settings");
    }
    set_policy("permit");
    println!("PASS config editor applied, conflict, forbidden and unavailable with unchanged revision");

    let reader = machine.resolve_config(vec![], abstraction_facade_native::Scope::Local).unwrap();
    let observer = machine.resolve_config_observer(vec![], abstraction_facade_native::Scope::Local).unwrap();
    assert_eq!(reader.read(config::RunOverrides::default()).unwrap().off["rust-edit"], "applied");
    let first = observer.observe(config::RunOverrides::default(), "", 0).unwrap();
    assert!(first.outcome == "snapshot" && !first.cursor.is_empty(), "first observation {}", first.outcome);
    assert_eq!(first.snapshot.as_ref().unwrap().off["rust-edit"], "applied");
    let unchanged = observer.observe(config::RunOverrides::default(), &first.cursor, 0).unwrap();
    assert!(unchanged.outcome == "unchanged" && unchanged.snapshot.is_none() && unchanged.cursor == first.cursor);
    let current = editor.read_user().unwrap();
    let mut observed = settings(&current.values);
    observed.off.insert("rust-observed".into(), "yes".into());
    assert_eq!(editor.replace_user(&current.revision, observed).unwrap().outcome, "applied");
    let changed = observer.observe(config::RunOverrides::default(), &first.cursor, 3000).unwrap();
    assert!(changed.outcome == "snapshot" && changed.cursor != first.cursor, "waiting observation {}", changed.outcome);
    assert_eq!(changed.snapshot.as_ref().unwrap().off["rust-observed"], "yes");
    assert!(matches!(observer.observe(config::RunOverrides::default(), &changed.cursor, 30001), Err(ConfigError::Invalid(_))));
    assert_eq!(reader.read(config::RunOverrides::default()).unwrap().off["rust-observed"], "yes");
    println!("PASS config reader and observer: snapshot, unchanged, waiting snapshot after an edit, local bound refusal");

    let questions = machine.resolve_asks(vec![], abstraction_facade_native::Scope::Local).unwrap();
    let operator = machine.resolve_asks_operator(vec![], abstraction_facade_native::Scope::Local).unwrap();
    let kept = questions.ask(question("rust-answered", "kept.example")).unwrap();
    assert_eq!(kept.outcome, "pending");
    let kept_id = kept.answer.as_ref().unwrap().id.clone();
    assert_eq!(questions.ask(question("rust-answered", "kept.example")).unwrap().answer.unwrap().id, kept_id);
    assert_eq!(questions.ask(question("rust-answered", "other.example")).unwrap().outcome, "conflict");
    assert_eq!(questions.observe("rust-answered", 0).unwrap().outcome, "pending");
    let retiring = questions.ask(question("rust-retired", "retired.example")).unwrap();
    let retiring_id = retiring.answer.unwrap().id;

    let page = operator.list("", 16).unwrap();
    assert!(page.outcome == "page" && page.complete && page.records.len() == 2);
    let decision = operator.answer(&kept_id, "once").unwrap();
    assert_eq!(decision.outcome, "answered");
    assert_eq!(decision.record.as_ref().unwrap().option, "once");
    let replay = operator.answer(&kept_id, "once").unwrap();
    assert_eq!(replay.record.unwrap().answered, decision.record.unwrap().answered);
    assert_eq!(operator.answer(&kept_id, "refuse").unwrap().outcome, "conflict");
    let observed = questions.observe("rust-answered", 0).unwrap();
    assert!(observed.outcome == "answered" && observed.answer.unwrap().option == "once");

    let retired = operator.retire(&retiring_id).unwrap();
    assert_eq!(retired.outcome, "retired");
    let record = retired.record.unwrap();
    assert!(record.id == retiring_id && record.option.is_empty());
    let retired_replay = operator.retire(&retiring_id).unwrap();
    assert!(retired_replay.outcome == "retired" && retired_replay.record.is_none());
    assert_eq!(questions.observe("rust-retired", 0).unwrap().outcome, "gone");
    assert_eq!(questions.ask(question("rust-retired", "retired.example")).unwrap().outcome, "gone");
    assert_eq!(operator.retire("rust-never-admitted").unwrap().outcome, "unknown");
    assert_eq!(operator.answer(&retiring_id, "once").unwrap().outcome, "unknown");
    assert!(matches!(operator.retire("bad\nid"), Err(AsksError::Invalid(_))));
    let remaining = operator.list("", 16).unwrap();
    assert!(remaining.records.len() == 1 && remaining.records[0].id == kept_id);
    println!("PASS questions ask/observe and operator list, answer, conflict, retire, replay, gone and unknown");
    println!("RETAINED {kept_id}");
}

fn main() {
    let a: Vec<String> = std::env::args().collect();
    if a.len() == 2 && a[1] == "--help" {
        println!("config-asks-consumer <runtime> all <policy-file>\nconfig-asks-consumer <runtime> operator-forbidden <id>\nconfig-asks-consumer <runtime> still-answered");
        return;
    }
    assert!(a.len() >= 3, "usage: config-asks-consumer <runtime> <mode> [argument]");
    let machine = Machine::new(&a[1]);
    match a[2].as_str() {
        "all" => config_and_questions(&machine, &a[3]),
        "operator-forbidden" => {
            let operator = machine.resolve_asks_operator(vec![], abstraction_facade_native::Scope::Local).unwrap();
            let page = operator.list("", 16).unwrap();
            assert!(page.outcome == "forbidden" && page.records.is_empty() && page.next.is_empty() && !page.complete);
            let decision = operator.answer(&a[3], "refuse").unwrap();
            assert!(decision.outcome == "forbidden" && decision.record.is_none());
            let retired = operator.retire(&a[3]).unwrap();
            assert!(retired.outcome == "forbidden" && retired.record.is_none());
            println!("PASS another program is refused list, answer and retire");
        }
        "still-answered" => {
            let questions = machine.resolve_asks(vec![], abstraction_facade_native::Scope::Local).unwrap();
            let observed = questions.observe("rust-answered", 0).unwrap();
            assert!(observed.outcome == "answered" && observed.answer.unwrap().option == "once");
            println!("PASS refused operator left the answer unchanged");
        }
        other => panic!("unknown mode {other}"),
    }
}
