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
    if args.len() != 3 {
        println!("usage: rust-jobs-consumer ENDPOINT GENERATED_REQUEST_HEX");
        return;
    }
    let deadline = Instant::now() + Duration::from_secs(15);
    let machine = Machine::new(&args[1]).with_deadline(deadline);
    let client = machine.resolve_jobs(vec![], "local").unwrap();
    assert!(client.owner().is_empty());
    let client = client.with_waiting(Some(deadline), None).unwrap();
    assert!(client.owner().is_empty());
    let window = client.history_window().unwrap();
    assert_eq!(window.logical_owner, "rust-jobs-owner");
    assert_ne!(window.logical_owner, "implementation-A");
    let identity = wire::RequestIdentity {
        key: "retained-rust-key".into(),
        history_epoch: window.history_epoch,
    };
    let spec: Vec<u8> = args[2]
        .as_bytes()
        .chunks(2)
        .map(|p| u8::from_str_radix(std::str::from_utf8(p).unwrap(), 16).unwrap())
        .collect();
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
        },
        kind: "download".into(),
        spec,
        required_guarantees: vec![],
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
        NativeConnector,
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
        })
        .unwrap();
    assert_eq!(unknown.outcome, "unknown");
    assert_eq!(
        restored.cancel_work(&identity).unwrap().outcome,
        "already_terminal"
    );
    let page = machine
        .resolve_job_inventory(vec![], "local")
        .unwrap()
        .list("", 1)
        .unwrap();
    assert_eq!(page.outcome, "page");
    assert_eq!(page.snapshots[0].receipt.operation_id, receipt.operation_id);
    println!("PASS: Rust real lost reply/reconcile, owner retention, cancelled wait, exact bounded result, unknown, explicit cancellation and inventory");
}
