use abstraction_facade_jobs::{
    jobs::{wire, Error, Jobs},
    JobsMachine,
};
use abstraction_facade_native::{Cancellation, Machine, NativeConnector};
use std::{
    sync::{
        atomic::{AtomicBool, Ordering},
        Arc,
    },
    time::{Duration, Instant},
};
#[derive(Clone)]
struct LoseReply {
    inner: abstraction_ipc::FrameTransport,
    lost: Arc<AtomicBool>,
}
impl wire::FrameTransport for LoseReply {
    type Error = abstraction_ipc::Error;
    fn write_frame(&self, b: &[u8]) -> Result<(), Self::Error> {
        wire::FrameTransport::write_frame(&self.inner, b)
    }
    fn exchange_frame(&self, b: &[u8]) -> Result<Vec<u8>, Self::Error> {
        let reply = wire::FrameTransport::exchange_frame(&self.inner, b)?;
        if !self.lost.swap(true, Ordering::SeqCst) {
            Err(abstraction_ipc::Error {
                status: abstraction_ipc::DISCONNECTED,
                transferred: 0,
                message: "fixture discarded real reply",
            })
        } else {
            Ok(reply)
        }
    }
}
fn main() {
    let args: Vec<_> = std::env::args().collect();
    if args.len() != 6 {
        println!("usage: rust-jobs-consumer ENDPOINT GENERATED_REQUEST_HEX UNMETERED_REQUEST_HEX CREDENTIAL_REQUEST_HEX MISSING_CREDENTIAL_REQUEST_HEX");
        return;
    }
    let deadline = Instant::now() + Duration::from_secs(15);
    let machine = Machine::new(&args[1]).with_deadline(deadline);
    let client = machine.resolve_jobs(vec![], abstraction_facade_native::Scope::Local).unwrap();
    assert!(client.owner().is_empty());
    let client = client.with_waiting(Some(deadline), None).unwrap();
    assert!(client.owner().is_empty());
    let window = client.history_window().unwrap();
    assert_eq!(window.logical_owner, "rust-jobs-owner");
    assert_ne!(window.logical_owner, "implementation-A");
    let identity = wire::RequestIdentity {
        key: "retained-rust-key".into(),
        history_epoch: window.history_epoch,
        attempt: 0,
    };
    let unhex = |text: &str| -> Vec<u8> {
        text.as_bytes()
            .chunks(2)
            .map(|p| u8::from_str_radix(std::str::from_utf8(p).unwrap(), 16).unwrap())
            .collect()
    };
    let spec = unhex(&args[2]);
    let transport = abstraction_ipc::FrameTransport::new(client.endpoint(), Duration::from_secs(3))
        .unwrap()
        .with_deadline(deadline)
        .with_limit(2 * 1024 * 1024)
        .unwrap();
    let lost = Jobs::new(
        LoseReply {
            inner: transport,
            lost: Arc::new(AtomicBool::new(false)),
        },
        client.owner(),
        vec![],
    )
    .unwrap();
    let submission = wire::Submission {
        identity: wire::RequestIdentity {
            key: identity.key.clone(),
            history_epoch: identity.history_epoch.clone(),
            attempt: identity.attempt,
        },
        kind: "download".into(),
        spec,
        required_guarantees: vec![],
        label: "rust fixture \u{b7} generated request".into(),
    };
    assert!(matches!(
        lost.submit(submission),
        Err(Error::Call(wire::CallError::Transport(_)))
    ));
    let accepted = client.reconcile(&identity).unwrap();
    assert_eq!(accepted.outcome, "accepted");
    let receipt = accepted.receipt.unwrap();
    assert_eq!(receipt.logical_owner, window.logical_owner);
    let signal = Cancellation::new().unwrap();
    signal.signal();
    assert!(
        matches!(client.with_waiting(Some(deadline),Some(signal)).unwrap().observe(&identity),Err(Error::Call(wire::CallError::Transport(e))) if e.status==abstraction_ipc::CANCELLED)
    );
    let restored = Jobs::restore(
        NativeConnector::default(),
        client.endpoint(),
        client.owner(),
        receipt.accepted_guarantees,
        Some(deadline),
        None,
    )
    .unwrap();
    std::env::set_var("ABSTRACTION_RUNTIME_ENDPOINT", "must-not-rediscover");
    loop {
        let s = restored.observe(&identity).unwrap().snapshot.unwrap();
        assert!(!s.cancellation_requested);
        if s.state == "complete" {
            break;
        }
        assert!(Instant::now() < deadline);
        std::thread::sleep(Duration::from_millis(10));
    }
    let mut bytes = Vec::new();
    let count = restored.copy_result(&identity, &mut bytes).unwrap();
    let expected: Vec<u8> = (0..256 * 1025).map(|i| i as u8).collect();
    assert_eq!(bytes, expected);
    assert_eq!(count, expected.len() as u64);
    let unknown = restored
        .reconcile(&wire::RequestIdentity {
            key: "unseen".into(),
            history_epoch: format!("{}-unknown", identity.history_epoch),
            attempt: 0,
        })
        .unwrap();
    assert_eq!(unknown.outcome, "unknown");
    assert_eq!(
        restored.cancel_work(&identity).unwrap().outcome,
        "already_terminal"
    );
    let page = machine
        .resolve_job_inventory(vec![], abstraction_facade_native::Scope::Local)
        .unwrap()
        .list("", 1)
        .unwrap();
    assert_eq!(page.outcome, "page");
    assert_eq!(page.snapshots[0].receipt.operation_id, receipt.operation_id);
    // The caller label sent with the lost reply is stored and listed (JOB-A12).
    assert_eq!(page.snapshots[0].label, "rust fixture \u{b7} generated request");
    assert!(!page.snapshots[0].label_derived);
    // The runtime's cost source reports a metered path. A request with network
    // unmetered requires network-cost@1, waits with the word network:metered
    // and is cancelled with nothing fetched (download DL-N2 to DL-N6, JOB-A15).
    const NETWORK_COST: &str = "abstraction.download/network-cost@1";
    let waiting_identity = || wire::RequestIdentity {
        key: "waiting-rust-key".into(),
        history_epoch: identity.history_epoch.clone(),
        attempt: 0,
    };
    let accepted = restored
        .submit(wire::Submission {
            identity: waiting_identity(),
            kind: "download".into(),
            spec: unhex(&args[3]),
            required_guarantees: vec![NETWORK_COST.into()],
            label: String::new(),
        })
        .unwrap();
    assert_eq!(accepted.outcome, "accepted");
    assert!(accepted
        .receipt
        .unwrap()
        .accepted_guarantees
        .iter()
        .any(|g| g == NETWORK_COST));
    loop {
        let s = restored.observe(&waiting_identity()).unwrap().snapshot.unwrap();
        // The word is written while the lease is held; the work reads pending
        // once the lease is released.
        if s.waiting == "network:metered" && s.state == "pending" {
            break;
        }
        assert!(s.state == "pending" || s.state == "running");
        assert!(Instant::now() < deadline);
        std::thread::sleep(Duration::from_millis(20));
    }
    assert_eq!(
        restored.cancel_work(&waiting_identity()).unwrap().outcome,
        "requested"
    );
    loop {
        let s = restored.observe(&waiting_identity()).unwrap().snapshot.unwrap();
        if s.state == "cancelled" {
            assert!(s.waiting.is_empty());
            break;
        }
        assert!(Instant::now() < deadline);
        std::thread::sleep(Duration::from_millis(20));
    }
    // The runtime admits the credential hf and refuses applying it as revoked:
    // the operation fails permanently with cause credential and the applier
    // outcome. A name it does not hold is refused at admission with no receipt
    // (job JOB-A8, JOB-A16; download DL-K1).
    const CREDENTIALS: &str = "abstraction.download/credentials@1";
    let named_identity = |key: &str| wire::RequestIdentity {
        key: key.into(),
        history_epoch: identity.history_epoch.clone(),
        attempt: 0,
    };
    let accepted = restored
        .submit(wire::Submission {
            identity: named_identity("credential-rust-key"),
            kind: "download".into(),
            spec: unhex(&args[4]),
            required_guarantees: vec![CREDENTIALS.into()],
            label: String::new(),
        })
        .unwrap();
    assert_eq!(accepted.outcome, "accepted");
    let failed = loop {
        let s = restored
            .observe(&named_identity("credential-rust-key"))
            .unwrap()
            .snapshot
            .unwrap();
        if s.state == "failed" {
            break s;
        }
        assert!(s.state == "pending" || s.state == "running");
        assert!(Instant::now() < deadline);
        std::thread::sleep(Duration::from_millis(20));
    };
    let failure = failed.failure.unwrap();
    assert_eq!(failure.classification, wire::FailureClass::Permanent);
    assert_eq!(failure.cause, wire::failure_cause::CREDENTIAL);
    assert_eq!(failure.message, "download attempt failed: credential:revoked:hf");
    let refused = restored
        .submit(wire::Submission {
            identity: named_identity("missing-rust-key"),
            kind: "download".into(),
            spec: unhex(&args[5]),
            required_guarantees: vec![CREDENTIALS.into()],
            label: String::new(),
        })
        .unwrap();
    assert_eq!(refused.outcome, "invalid");
    assert_eq!(refused.reason, "credential:unknown:missing");
    assert!(refused.receipt.is_none());
    println!("PASS: Rust real lost reply/reconcile, owner retention, cancelled wait, exact bounded result, unknown, explicit cancellation, inventory, label, network:metered wait, credential cause and admission refusal");
}
