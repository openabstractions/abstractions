use abstraction_facade_logging::{
    logging::{self, Sink},
    LoggingMachine,
};
use abstraction_facade_native::{Cancellation, Machine};
use abstraction_facade_service::Error;
use std::{
    collections::BTreeMap,
    time::{Duration, Instant},
};
fn main() {
    let args: Vec<String> = std::env::args().collect();
    if args.len() == 1 || args[1] == "--help" {
        println!("consumer runtime|absent|cancel|forged ENDPOINT");
        return;
    }
    let mode = &args[1];
    let endpoint = &args[2];
    if mode == "absent" {
        assert!(Machine::new(endpoint).resolve_log(vec![], "local").is_err());
        return;
    }
    if mode == "forged" {
        assert!(matches!(
            Machine::new(endpoint).resolve_log(vec![], "local"),
            Err(Error::InvalidResolution)
        ));
        return;
    }
    if mode == "cancel" {
        let signal = Cancellation::new().unwrap();
        let other = signal.clone();
        let worker = std::thread::spawn(move || {
            std::thread::sleep(Duration::from_millis(80));
            other.signal();
        });
        let start = Instant::now();
        let m = Machine::new(endpoint)
            .with_deadline(start + Duration::from_secs(2))
            .with_cancellation(signal);
        match m.resolve_log(vec![], "local") {
            Err(Error::Call(abstraction_facade_service::wire::CallError::Transport(e))) => {
                assert_eq!(e.status, abstraction_ipc::CANCELLED)
            }
            _ => panic!("cancellation lost"),
        };
        worker.join().unwrap();
        assert!(start.elapsed() < Duration::from_secs(1));
        return;
    }
    // Native bootstrap reads the fixture's explicit override; no default owner runtime.
    let m = abstraction_facade_native::discover()
        .unwrap()
        .with_deadline(Instant::now() + Duration::from_secs(8));
    assert!(
        matches!(m.resolve_log(vec!["unavailable-guarantee".into()],"local"),Err(Error::ResolutionStatus(s)) if s=="unmet_requirements")
    );
    let logger = m.resolve_log(vec![], "local").unwrap();
    let record = || logging::Record {
        schema: 1,
        time: "2026-09-12T12:00:00.000000Z".into(),
        level: 2,
        msg: "rust shared IPC \u{2713}".into(),
        attrs: BTreeMap::from([("component".into(), "outside-consumer".into())]),
        ..Default::default()
    };
    let mut invalid = record();
    invalid.schema = 2;
    match logger.Write(invalid) {
        Err(logging::CallError::Refusal(r)) => assert_eq!(r.word, "bad_schema"),
        _ => panic!("outbound invalid schema reached transport"),
    }
    logger.Write(record()).unwrap();
    let history = m.resolve_log_reader(vec![], "local").unwrap();
    let page = history.read("".into(), 16, 65536).unwrap();
    assert_eq!(page.outcome, "page");
    assert_eq!(page.records.len(), 1);
    let r = &page.records[0];
    assert_eq!(r.schema, 1);
    assert_eq!(r.time, record().time);
    assert_eq!(r.level, 2);
    assert_eq!(r.msg, record().msg);
    assert_eq!(r.attrs, record().attrs);
    assert!(!r.identity.is_empty());
    let end = history.read(page.next, 16, 65536).unwrap();
    assert_eq!(end.outcome, "page");
    assert!(end.records.is_empty() && end.at_end);
    assert!(history.read("".into(), 0, 65536).is_err());
    let signal = Cancellation::new().unwrap();
    let bound = Machine::new(endpoint)
        .with_cancellation(signal.clone())
        .resolve_log(vec![], "local")
        .unwrap();
    signal.signal();
    match bound.Write(record()) {
        Err(logging::CallError::Transport(e)) => assert_eq!(e.status, abstraction_ipc::CANCELLED),
        _ => panic!("binding lost cancellation"),
    };
    let timed = Machine::new(endpoint)
        .with_deadline(Instant::now() + Duration::from_millis(150))
        .resolve_log(vec![], "local")
        .unwrap();
    std::thread::sleep(Duration::from_millis(180));
    match timed.Write(record()) {
        Err(logging::CallError::Transport(e)) => assert_eq!(e.status, abstraction_ipc::TIMEOUT),
        _ => panic!("binding lost original deadline"),
    };
    println!("PASS resolved Rust logging exact record/history, unmet, cancellation, deadline");
}
