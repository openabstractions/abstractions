//! Time N identical caller@1 Observe calls from one Rust process at a runtime
//! resolver endpoint through abstraction-ipc's FrameTransport: single opens a
//! connection per call, session keeps one. Rust has no generated facade caller
//! client, so the request is the encoded frame run.py passes in hex, and the
//! reply is checked for the observed outcome without a full decode.
//!
//!   rust-percall <runtime endpoint> <calls> <warmup> single|session <request hex>
use std::time::{Duration, Instant};

fn main() {
    let args: Vec<String> = std::env::args().collect();
    if args.len() != 6 || !["single", "session"].contains(&args[4].as_str()) {
        println!("rust-percall <runtime endpoint> <calls> <warmup> single|session <request hex>");
        std::process::exit(if args.get(1).map(String::as_str) == Some("--help") { 0 } else { 2 });
    }
    let calls: usize = args[2].parse().expect("calls");
    let warmup: usize = args[3].parse().expect("warmup");
    let session = args[4] == "session";
    let hex = args[5].as_bytes();
    let request: Vec<u8> = hex
        .chunks(2)
        .map(|pair| u8::from_str_radix(std::str::from_utf8(pair).unwrap(), 16).expect("request hex"))
        .collect();
    let transport = abstraction_ipc::FrameTransport::new(args[1].as_str(), Duration::from_secs(10))
        .expect("transport")
        .with_sessions(session);
    let mut samples = Vec::with_capacity(calls);
    let (mut first, mut code) = (0.0, String::new());
    for i in 0..warmup + calls {
        let began = Instant::now();
        let reply = transport.exchange_frame(&request).expect("observe");
        let took = began.elapsed().as_secs_f64() * 1000.0;
        let text = String::from_utf8_lossy(&reply);
        if !text.contains("\"observed\"") {
            eprintln!("observe reply without an observed outcome: {text}");
            std::process::exit(1);
        }
        if i == 0 {
            first = took;
            if let Some(at) = text.find("\"code\"") {
                let marker = "\"proof\"";
                if let Some(proof) = text[at..].find(marker) {
                    code = text[at + proof + marker.len()..]
                        .split('"')
                        .nth(1)
                        .unwrap_or("")
                        .to_string();
                }
            }
        }
        if i >= warmup {
            samples.push(took);
        }
    }
    samples.sort_by(|a, b| a.partial_cmp(b).unwrap());
    let rank = |p: f64| samples[((samples.len() as f64 * p).ceil() as usize).max(1) - 1];
    println!(
        "PERCALL {} calls={} first={:.3} p50={:.3} p90={:.3} p99={:.3} max={:.3} code={}",
        if session { "rust-session" } else { "rust" },
        samples.len(),
        first,
        rank(0.5),
        rank(0.9),
        rank(0.99),
        samples[samples.len() - 1],
        code
    );
}
