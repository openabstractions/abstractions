use abstraction_ipc::{
    runtime_endpoint, Cancellation, FrameTransport, CANCELLED, DISCONNECTED, INVALID_ARGUMENT,
    TIMEOUT,
};
use std::{
    thread,
    time::{Duration, Instant},
};
fn main() {
    let args: Vec<String> = std::env::args().collect();
    let mode = &args[1];
    let endpoint = &args[2];
    if mode == "bootstrap" {
        assert_eq!(runtime_endpoint().unwrap(), *endpoint);
        println!("PASS bootstrap");
        return;
    }
    let transport = FrameTransport::new(endpoint, Duration::from_secs(2)).unwrap();
    let payload = [0, 255, 10, 0, 65];
    match mode.as_str() {
        "echo" => assert_eq!(transport.exchange_frame(&payload).unwrap(), payload),
        "oversized" => assert_eq!(
            transport.exchange_frame(&payload).unwrap_err().status,
            INVALID_ARGUMENT
        ),
        "header" | "body" => assert_eq!(
            transport.exchange_frame(&payload).unwrap_err().status,
            DISCONNECTED
        ),
        "absent" => assert!(transport.exchange_frame(&payload).is_err()),
        "oneway" => transport.write_frame(&payload).unwrap(),
        "cancel" => {
            let signal = Cancellation::new().unwrap();
            let other = signal.clone();
            let worker = thread::spawn(move || {
                thread::sleep(Duration::from_millis(80));
                other.signal();
            });
            let bound = transport.with_cancellation(signal);
            let start = Instant::now();
            assert_eq!(
                bound.exchange_frame(&payload).unwrap_err().status,
                CANCELLED
            );
            assert!(start.elapsed() < Duration::from_secs(1));
            worker.join().unwrap();
        }
        "timeout" => {
            let bound = transport.with_deadline(Instant::now() + Duration::from_millis(80));
            assert_eq!(bound.exchange_frame(&payload).unwrap_err().status, TIMEOUT);
        }
        _ => panic!("invalid mode"),
    }
    println!("PASS {}", mode);
}
