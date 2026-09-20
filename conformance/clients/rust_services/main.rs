use abstraction_facade_logging::{
    logging::{self, Sink},
    LoggingMachine,
};
use abstraction_facade_native::{Cancellation, Machine, NativeConnector, TransportError};
use abstraction_facade_service::{Connector, Error, ResolutionFailure};
use std::{
    collections::BTreeMap,
    time::{Duration, Instant},
};

/// The installed connector with its runtime endpoint moved to the one given as
/// `--runtime-endpoint`, as ABSTRACTION_RUNTIME_ENDPOINT moves it. Selection and
/// identity verification stay the installed connector's, so the option supplies
/// no trust. On Windows the native bootstrap reads the variable through the C
/// runtime, which does not see `std::env::set_var`; the option needs no child
/// process to set it.
#[derive(Clone)]
struct EndpointOverride {
    native: NativeConnector,
    endpoint: String,
}
impl Connector for EndpointOverride {
    type Transport = <NativeConnector as Connector>::Transport;
    type Cancellation = Cancellation;
    fn supports(&self, scope: abstraction_facade_native::Scope, transport: &str) -> bool {
        self.native.supports(scope, transport)
    }
    fn connect(
        &self,
        endpoint: &str,
        deadline: Instant,
        cancellation: Option<Cancellation>,
        max_frame: usize,
    ) -> Result<Self::Transport, TransportError> {
        self.native.connect(endpoint, deadline, cancellation, max_frame)
    }
    fn runtime_endpoint(&self) -> Result<String, TransportError> {
        Ok(self.endpoint.clone())
    }
    fn select_installed(
        &self,
        deadline: Instant,
        cancellation: Option<Cancellation>,
    ) -> Result<Self, TransportError> {
        Ok(Self {
            native: self.native.select_installed(deadline, cancellation)?,
            endpoint: self.endpoint.clone(),
        })
    }
    fn is_cancellation(&self, error: &TransportError) -> bool {
        self.native.is_cancellation(error)
    }
}

fn main() {
    let mut args: Vec<String> = std::env::args().collect();
    if args.len() == 1 || args[1] == "--help" {
        println!("consumer runtime|absent|cancel|forged ENDPOINT [--runtime-endpoint OVERRIDE]");
        println!("  --runtime-endpoint OVERRIDE  runtime mode: the endpoint override, in place of ABSTRACTION_RUNTIME_ENDPOINT");
        return;
    }
    let runtime_endpoint = match args.iter().position(|a| a == "--runtime-endpoint") {
        Some(at) => {
            assert!(at + 1 < args.len(), "--runtime-endpoint needs a value");
            let value = args.remove(at + 1);
            args.remove(at);
            Some(value)
        }
        None => None,
    };
    let mode = &args[1];
    let endpoint = &args[2];
    if mode == "absent" {
        // Nobody listens: the facade's resolution error, with the transport failure as its source.
        match Machine::new(endpoint).resolve_log(vec![], abstraction_facade_native::Scope::Local) {
            Err(error @ Error::Resolution(_)) => {
                assert!(std::error::Error::source(&error).is_some(), "transport failure lost: {error}");
                let Error::Resolution(r) = error else { unreachable!() };
                assert_eq!(r.failure, ResolutionFailure::RuntimeUnavailable);
                assert_eq!(r.looked_for, format!("the explicit endpoint {endpoint}"));
                assert_eq!((r.capability.as_str(), r.contract.as_str()), ("abstraction.logging", "abstraction.logging/sink@1"));
            }
            Err(other) => panic!("absent runtime surfaced {other:?}"),
            Ok(_) => panic!("absent runtime resolved"),
        }
        println!("PASS absent: runtime_unavailable at the explicit endpoint, with source");
        return;
    }
    if mode == "forged" {
        assert!(matches!(
            Machine::new(endpoint).resolve_log(vec![], abstraction_facade_native::Scope::Local),
            Err(Error::Resolution(r)) if r.failure == ResolutionFailure::InvalidResolution
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
        match m.resolve_log(vec![], abstraction_facade_native::Scope::Local) {
            Err(Error::Call(abstraction_facade_service::wire::CallError::Transport(e))) => {
                assert_eq!(e.status, abstraction_ipc::CANCELLED)
            }
            _ => panic!("cancellation lost"),
        };
        worker.join().unwrap();
        assert!(start.elapsed() < Duration::from_secs(1));
        return;
    }
    // Installed discovery selects the runtime's identity through the shared C ABI.
    // The endpoint override moves the endpoint and supplies no trust, so the
    // fixture host, which is never the selected installation, is refused. The
    // override comes from the option when one is given, else from the variable.
    let deadline = Instant::now() + Duration::from_secs(8);
    let installed = match &runtime_endpoint {
        Some(option) => {
            let connector = EndpointOverride {
                native: NativeConnector::default(),
                endpoint: option.clone(),
            };
            assert_eq!(connector.runtime_endpoint().unwrap(), *endpoint);
            abstraction_facade_service::Machine::installed_with_connector(connector)
                .with_deadline(deadline)
                .resolve_log(vec![], abstraction_facade_native::Scope::Local)
                .map(|_| ())
        }
        None => {
            assert_eq!(abstraction_ipc::runtime_endpoint().unwrap(), *endpoint);
            abstraction_facade_native::discover()
                .with_deadline(deadline)
                .resolve_log(vec![], abstraction_facade_native::Scope::Local)
                .map(|_| ())
        }
    };
    let refusal = match installed {
        Err(Error::Resolution(r)) => {
            assert_eq!(r.failure, ResolutionFailure::RuntimeUnavailable);
            let status = r.cause.as_ref().map(|e| e.status);
            assert!(
                matches!(status, Some(abstraction_ipc::UNTRUSTED | abstraction_ipc::PROOF_UNAVAILABLE)),
                "installed discovery reached the fixture host: {:?} at {}",
                r.cause,
                r.looked_for
            );
            format!("{} at {}", r.cause.unwrap(), r.looked_for)
        }
        Err(other) => panic!("installed discovery surfaced {other:?}"),
        Ok(_) => panic!("installed discovery trusted an endpoint override"),
    };
    let via = if runtime_endpoint.is_some() { "option" } else { "variable" };
    println!("PASS installed discovery verifies the selected runtime identity; the endpoint override ({via}) is refused: {refusal}");
    let m = Machine::new(endpoint).with_deadline(Instant::now() + Duration::from_secs(8));
    assert!(
        matches!(m.resolve_log(vec!["unavailable-guarantee".into()],abstraction_facade_native::Scope::Local),Err(Error::Resolution(r)) if r.failure.status()=="unmet_requirements" && r.cause.is_none())
    );
    let logger = m.resolve_log(vec![], abstraction_facade_native::Scope::Local).unwrap();
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
    match logger.write(invalid) {
        Err(logging::CallError::Refusal(r)) => assert_eq!(r.word, "bad_schema"),
        _ => panic!("outbound invalid schema reached transport"),
    }
    logger.write(record()).unwrap();
    let history = m.resolve_log_reader(vec![], abstraction_facade_native::Scope::Local).unwrap();
    let page = history.read("".into(), 16, 65536).unwrap();
    assert_eq!(page.outcome, "page");
    assert_eq!(page.records.len(), 1);
    let r = &page.records[0];
    assert_eq!(r.schema, 1);
    assert_eq!(r.time, record().time);
    assert_eq!(r.level, 2);
    assert_eq!(r.msg, record().msg);
    // A record submitted without a writer claim gains an explicit unclaimed marker (logging CONTRACT).
    let mut expected_attrs = record().attrs;
    expected_attrs.insert("logging.writer_claim".into(), "absent".into());
    assert_eq!(r.attrs, expected_attrs);
    assert!(!r.identity.is_empty());
    let end = history.read(page.next, 16, 65536).unwrap();
    assert_eq!(end.outcome, "page");
    assert!(end.records.is_empty() && end.at_end);
    assert!(history.read("".into(), 0, 65536).is_err());
    // The host's history policy reads this file on every call.
    if let Ok(mode_file) = std::env::var("OA_RUST_HISTORY_POLICY") {
        for (mode, code) in [("history-forbidden", "forbidden"), ("history-unavailable", "policy_unavailable")] {
            std::fs::write(&mode_file, mode).unwrap();
            match history.read("".into(), 16, 65536) {
                Err(logging::CallError::Service(e)) => assert_eq!(e.code, code),
                other => panic!("{mode} history read gave {:?}", other.map(|p| p.outcome)),
            }
        }
        std::fs::write(&mode_file, "permit").unwrap();
        assert_eq!(history.read("".into(), 16, 65536).unwrap().outcome, "page");
        println!("PASS Rust history refusal codes forbidden and policy_unavailable");
    }
    let signal = Cancellation::new().unwrap();
    let bound = Machine::new(endpoint)
        .with_cancellation(signal.clone())
        .resolve_log(vec![], abstraction_facade_native::Scope::Local)
        .unwrap();
    signal.signal();
    match bound.write(record()) {
        Err(logging::CallError::Transport(e)) => assert_eq!(e.status, abstraction_ipc::CANCELLED),
        _ => panic!("binding lost cancellation"),
    };
    let timed = Machine::new(endpoint)
        .with_deadline(Instant::now() + Duration::from_millis(150))
        .resolve_log(vec![], abstraction_facade_native::Scope::Local)
        .unwrap();
    std::thread::sleep(Duration::from_millis(180));
    match timed.write(record()) {
        Err(logging::CallError::Transport(e)) => assert_eq!(e.status, abstraction_ipc::TIMEOUT),
        _ => panic!("binding lost original deadline"),
    };
    println!("PASS resolved Rust logging exact record/history, unmet, cancellation, deadline");
}
