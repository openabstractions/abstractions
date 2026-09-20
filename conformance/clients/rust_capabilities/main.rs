use abstraction_facade_logging::{
    logging::{self, Sink},
    LoggingMachine,
};
use abstraction_facade_model::{wire as model, ModelMachine};
use abstraction_facade_rights::{wire as rights, Error as RightsError, RightsMachine};
use std::collections::BTreeMap;
use std::time::{Duration, Instant};
use abstraction_facade_native::Machine;
use abstraction_facade_router::{wire as router, Error as RouterError, RouterMachine};
use abstraction_facade_storage::{new_request_id, Error as StorageError, StorageMachine};

// Test-only SHA-256 so the consumer names its own content without registry crates.
fn sha256(data: &[u8]) -> String {
    const K: [u32; 64] = [
        0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5,
        0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3, 0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174,
        0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc, 0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da,
        0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7, 0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967,
        0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13, 0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85,
        0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3, 0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070,
        0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5, 0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3,
        0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208, 0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2,
    ];
    let mut h: [u32; 8] = [0x6a09e667, 0xbb67ae85, 0x3c6ef372, 0xa54ff53a, 0x510e527f, 0x9b05688c, 0x1f83d9ab, 0x5be0cd19];
    let mut m = data.to_vec();
    m.push(0x80);
    while m.len() % 64 != 56 {
        m.push(0);
    }
    m.extend_from_slice(&((data.len() as u64) * 8).to_be_bytes());
    for block in m.chunks(64) {
        let mut w = [0u32; 64];
        for i in 0..16 {
            w[i] = u32::from_be_bytes([block[4 * i], block[4 * i + 1], block[4 * i + 2], block[4 * i + 3]]);
        }
        for i in 16..64 {
            w[i] = w[i - 16].wrapping_add(w[i - 15].rotate_right(7) ^ w[i - 15].rotate_right(18) ^ (w[i - 15] >> 3))
                .wrapping_add(w[i - 7])
                .wrapping_add(w[i - 2].rotate_right(17) ^ w[i - 2].rotate_right(19) ^ (w[i - 2] >> 10));
        }
        let [mut a, mut b, mut c, mut d, mut e, mut f, mut g, mut hh] = h;
        for i in 0..64 {
            let t1 = hh
                .wrapping_add(e.rotate_right(6) ^ e.rotate_right(11) ^ e.rotate_right(25))
                .wrapping_add((e & f) ^ (!e & g))
                .wrapping_add(K[i])
                .wrapping_add(w[i]);
            let t2 = (a.rotate_right(2) ^ a.rotate_right(13) ^ a.rotate_right(22)).wrapping_add((a & b) ^ (a & c) ^ (b & c));
            hh = g;
            g = f;
            f = e;
            e = d.wrapping_add(t1);
            d = c;
            c = b;
            b = a;
            a = t1.wrapping_add(t2);
        }
        for (x, y) in h.iter_mut().zip([a, b, c, d, e, f, g, hh]) {
            *x = x.wrapping_add(y);
        }
    }
    "sha256:".to_string() + &h.iter().map(|x| format!("{x:08x}")).collect::<String>()
}

fn storage_changes(machine: &Machine, set_policy: &dyn Fn(&str)) {
    let changes = machine.resolve_storage_changes(vec![], abstraction_facade_native::Scope::Local).unwrap();
    let writer = machine.resolve_storage_writer(vec![], abstraction_facade_native::Scope::Local).unwrap();
    let store = |text: &str| {
        let digest = sha256(text.as_bytes());
        let stored = writer.write(&new_request_id().unwrap(), &digest, text.as_bytes()).unwrap();
        assert_eq!(stored.digest, digest);
        digest
    };
    for (mode, outcome) in [("changes-forbidden", "forbidden"), ("changes-unavailable", "unavailable")] {
        set_policy(mode);
        let refused = changes.observe("", 16, 0).unwrap();
        assert!(refused.outcome == outcome && refused.changes.is_empty() && refused.next.is_empty(), "{mode}");
        let listed = changes.list("", 16).unwrap();
        assert!(listed.outcome == outcome && listed.objects.is_empty() && listed.cursor.is_empty(), "{mode}");
        assert!(matches!(changes.snapshot(16), Err(StorageError::Outcome(o)) if o == outcome));
    }
    set_policy("permit");
    let (objects, cursor) = changes.snapshot(16).unwrap();
    assert!(objects.is_empty() && !cursor.is_empty());
    let first = store("rust observed object");
    let page = changes.observe(&cursor, 16, 3000).unwrap();
    assert_eq!(page.outcome, "page");
    assert_eq!(page.changes.iter().map(|c| (c.kind.as_str(), c.digest.as_str())).collect::<Vec<_>>(), vec![("added", first.as_str())]);

    set_policy("storage-unreadable");
    let hidden = store("rust object without read permission");
    let skipped = changes.observe(&page.next, 16, 300).unwrap();
    assert!(skipped.outcome == "page" && skipped.changes.iter().all(|c| c.digest != hidden), "unreadable object reported");
    assert_ne!(skipped.next, page.next, "skipped change did not advance the cursor");
    set_policy("permit");

    let stale = skipped.next.clone();
    let burst: Vec<String> = (0..6).map(|i| store(&format!("rust burst object {i}"))).collect();
    let gap = changes.observe(&stale, 16, 0).unwrap();
    assert!(gap.outcome == "gap" && gap.changes.is_empty() && gap.next == stale);
    let (objects, recovered) = changes.snapshot(2).unwrap();
    let mut listed: Vec<String> = objects.into_iter().map(|o| o.digest).collect();
    let mut expected: Vec<String> = [first, hidden].into_iter().chain(burst).collect();
    listed.sort();
    expected.sort();
    assert_eq!(listed, expected);
    let settled = changes.observe(&recovered, 16, 0).unwrap();
    assert!(settled.outcome == "page" && settled.changes.is_empty() && settled.at_end);
    assert!(matches!(changes.observe(&recovered, 257, 0), Err(StorageError::Invalid(_))));
    println!("PASS storage changes forbidden/unavailable, added change, unreadable skip with cursor advance, gap and paged snapshot recovery");
}

fn router_calls(machine: &Machine, set_policy: &dyn Fn(&str)) {
    let routes = machine.resolve_router(vec![], abstraction_facade_native::Scope::Local).unwrap();
    let pick = |model: &str| {
        let mut request = router::PickRequest::default();
        request.model = model.into();
        routes.pick(request)
    };
    // Live fake hosts shared with every router proof: resident Lemonade, cold LM Studio, unreachable Ollama.
    let models = routes.models(false).unwrap();
    let hosts = routes.hosts(false).unwrap();
    assert!(models.models.len() == 2 && hosts.hosts.len() == 3, "live inventory");
    assert!(
        models.models.iter().flat_map(|f| f.names.iter()).any(|a| a.host == "lemonade" && a.resident && a.servable),
        "resident alias lost"
    );
    assert!(
        hosts.hosts.iter().any(|h| h.host == "ollama" && !h.up && !h.why.is_empty() && h.installed == 0 && h.resident.is_empty()),
        "host failure hidden"
    );
    let audited = hosts.asked.len();
    let live = "qwen/qwen3.6-35b-a3b";
    let allowed = |names: &[&str]| {
        let mut request = router::PickRequest::default();
        request.model = live.into();
        request.allowed = Some(router::HostAllowance { hosts: names.iter().map(|h| h.to_string()).collect() });
        routes.pick(request).unwrap().decision
    };
    let resident = pick(live).unwrap().decision;
    assert!(
        resident.verdict == "resident" && resident.loads == 0 && !resident.endpoint.is_empty() && resident.authorised.is_none(),
        "resident pick {}",
        resident.verdict
    );
    let refused = allowed(&[]);
    assert!(
        refused.verdict == "unauthorised"
            && refused.endpoint.is_empty()
            && refused.authorised.as_ref().map_or(false, |a| a.hosts.is_empty())
            && !refused.withheld.is_empty(),
        "empty allowance {}",
        refused.verdict
    );
    let cold = allowed(&["lmstudio"]);
    assert!(
        cold.verdict == "would-load" && cold.loads == 1 && cold.host == "lmstudio" && !cold.endpoint.is_empty(),
        "cold pick {}",
        cold.verdict
    );
    let after = routes.hosts(false).unwrap();
    assert_eq!(after.asked.len(), audited + 3);
    assert!(
        after.asked[audited..].iter().all(|a| a.caller == after.observation.caller.path_description
            && a.user == after.observation.caller.user_description),
        "audit not bound to the Rust caller"
    );
    assert_eq!(pick("rust-model").unwrap().decision.asked, "rust-model");
    assert!(matches!(pick(""), Err(RouterError::Invalid(_))));
    for (mode, code) in [("router-forbidden", "forbidden"), ("router-unavailable", "policy_unavailable")] {
        set_policy(mode);
        assert_eq!(routes.models(false).err().and_then(|e| e.service_code().map(str::to_string)).as_deref(), Some(code), "{mode} models");
        assert_eq!(routes.hosts(false).err().and_then(|e| e.service_code().map(str::to_string)).as_deref(), Some(code), "{mode} hosts");
        assert_eq!(pick("rust-model").err().and_then(|e| e.service_code().map(str::to_string)).as_deref(), Some(code), "{mode} pick");
    }
    set_policy("permit");
    assert_eq!(pick("rust-model").unwrap().decision.asked, "rust-model");
    println!("PASS router live-host inventory, resident/unauthorised/would-load picks, caller-bound audit, forbidden and policy_unavailable codes");
}

fn log_observer(machine: &Machine, endpoint: &str, set_policy: &dyn Fn(&str)) {
    let observer = machine.resolve_log_observer(vec![], abstraction_facade_native::Scope::Local).unwrap();
    let mut cursor = String::new();
    loop {
        let page = observer.observe(&cursor, 256, 65536, 0).unwrap();
        assert_eq!(page.outcome, "page");
        cursor = page.next.clone();
        if page.at_end {
            break;
        }
    }
    // A separate binding writes after the observer has started waiting.
    let writer_endpoint = endpoint.to_string();
    let writer = std::thread::spawn(move || {
        std::thread::sleep(Duration::from_millis(300));
        let sink = Machine::new(&writer_endpoint).resolve_log(vec![], abstraction_facade_native::Scope::Local).unwrap();
        sink.write(logging::Record {
            schema: 1,
            time: "2026-09-15T12:00:00.000000Z".into(),
            level: 2,
            msg: "rust observed after waiting".into(),
            attrs: BTreeMap::from([("fixture".into(), "rust-observer".into())]),
            ..Default::default()
        })
        .unwrap();
    });
    let began = Instant::now();
    let page = observer.observe(&cursor, 16, 65536, 3000).unwrap();
    writer.join().unwrap();
    assert!(page.outcome == "page" && began.elapsed() >= Duration::from_millis(250), "observe returned without waiting");
    let observed = page.records.iter().find(|r| r.msg == "rust observed after waiting").expect("waiting observe missed the record");
    assert_eq!(observed.attrs["fixture"], "rust-observer");
    assert_ne!(page.next, cursor);
    let cursor = page.next;
    for (mode, code) in [("history-forbidden", "forbidden"), ("history-unavailable", "policy_unavailable")] {
        set_policy(mode);
        match observer.observe(&cursor, 16, 65536, 0) {
            Err(logging::CallError::Service(e)) => assert_eq!(e.code, code, "{mode}"),
            other => panic!("{mode} observation gave {:?}", other.map(|p| p.outcome)),
        }
    }
    set_policy("permit");
    assert!(matches!(observer.observe(&cursor, 16, 65536, 30001), Err(logging::CallError::Dispatch(_))));
    assert!(matches!(observer.observe(&cursor, 0, 65536, 0), Err(logging::CallError::Dispatch(_))));
    assert_eq!(observer.observe(&cursor, 16, 65536, 0).unwrap().outcome, "page");
    println!("PASS log observer waited for a later record, forbidden and policy_unavailable codes, local bound refusals");
}

fn rights_administration(machine: &Machine, set_policy: &dyn Fn(&str)) {
    let decisions = machine.resolve_rights(vec![], abstraction_facade_native::Scope::Local).unwrap();
    let admin = machine.resolve_rights_operator(vec![], abstraction_facade_native::Scope::Local).unwrap();
    let env = |name: &str| std::env::var(name).unwrap_or_else(|_| panic!("{name} required"));
    let me = || rights::Subject { account: env("OA_RIGHTS_ACCOUNT"), program: env("OA_RIGHTS_PROGRAM") };
    let (right, target) = ("fixture.read", "rust-resource");
    let rule = |permit: bool| rights::PolicyRule { subject: me(), action: right.into(), resource: target.into(), permit };
    let decide = || decisions.decide(right, target).unwrap();

    assert_eq!(decide().outcome, "not_granted");
    let unknown = decisions.decide("fixture.absent", target).unwrap();
    assert!(unknown.outcome == "unknown_action" && !unknown.policy_revision.is_empty());
    let page = admin.list_policy("", 64).unwrap();
    assert!(page.outcome == "page" && page.catalog == vec![right.to_string()] && page.rules.is_empty() && page.complete);
    let granted = admin.set_rule(&page.revision, rule(true)).unwrap();
    assert!(granted.outcome == "applied" && granted.current.as_ref().unwrap().permit && granted.revision != page.revision);
    let permitted = decide();
    assert!(permitted.outcome == "permitted" && !permitted.policy_revision.is_empty());
    assert_eq!(decisions.decide_for(me(), right, target).unwrap().outcome, "permitted");
    let stale = admin.set_rule(&page.revision, rule(false)).unwrap();
    assert!(stale.outcome == "conflict" && stale.revision == granted.revision && stale.current.as_ref().unwrap().permit);
    assert_eq!(decide().outcome, "permitted", "conflicting deny changed the decision");
    let denied = admin.set_rule(&granted.revision, rule(false)).unwrap();
    assert!(denied.outcome == "applied" && !denied.current.as_ref().unwrap().permit);
    assert_eq!(decide().outcome, "denied");
    let listed = admin.list_policy("", 64).unwrap();
    assert!(listed.rules.len() == 1 && listed.rules[0].subject.program == me().program && !listed.rules[0].permit);
    let revoked = admin.revoke_rule(&denied.revision, me(), right, target).unwrap();
    assert!(revoked.outcome == "applied" && revoked.current.is_none());
    assert_eq!(decide().outcome, "not_granted");
    let late = admin.revoke_rule(&denied.revision, me(), right, target).unwrap();
    assert!(late.outcome == "conflict" && late.current.is_none());
    for (mode, outcome) in [("operator-forbidden", "forbidden"), ("operator-unavailable", "unavailable")] {
        set_policy(mode);
        let refused = admin.list_policy("", 64).unwrap();
        assert!(refused.outcome == outcome && refused.revision.is_empty() && refused.rules.is_empty(), "{mode}");
        let refused_edit = admin.set_rule(&revoked.revision, rule(true)).unwrap();
        assert!(refused_edit.outcome == outcome && refused_edit.revision.is_empty(), "{mode}");
    }
    set_policy("permit");
    assert_eq!(decide().outcome, "not_granted", "refused edits changed policy");
    assert!(matches!(admin.set_rule("", rule(true)), Err(RightsError::Invalid(_))));
    let policy_file = env("OA_RIGHTS_POLICY_FILE");
    let saved = std::fs::read(&policy_file).ok();
    std::fs::write(&policy_file, b"{ not a policy").unwrap();
    let outage = decide();
    assert!(outage.outcome == "unavailable" && outage.policy_revision.is_empty(), "decision outage gave {}", outage.outcome);
    match saved {
        Some(bytes) => std::fs::write(&policy_file, bytes).unwrap(),
        None => std::fs::remove_file(&policy_file).unwrap(),
    }
    assert_eq!(decide().outcome, "not_granted");
    println!("PASS rights grant, deny, revoke, conflict at stale revisions, operator forbidden/unavailable, decision outage");
}

fn model_lookup(machine: &Machine, set_policy: &dyn Fn(&str)) {
    let lookup = machine.resolve_model(vec![], abstraction_facade_native::Scope::Local).unwrap();
    let reference = |repo: &str| {
        let mut r = model::Ref::default();
        r.registry = "fixture".into();
        r.repo = repo.into();
        r
    };
    let resolved = lookup.resolve(reference("weights")).unwrap();
    assert_eq!(resolved.outcome, "resolved");
    let request = resolved.request.unwrap();
    assert_eq!(request.artifact.digest, format!("sha256:{}", "c".repeat(64)));
    assert_eq!(request.artifact.size, 9);
    assert_eq!(request.sources.iter().map(|s| (s.scheme.as_str(), s.locator.as_str())).collect::<Vec<_>>(), vec![("http", "http://127.0.0.1/rust-weights")]);
    let private = lookup.resolve(reference("private")).unwrap();
    assert!(private.outcome == "unsupported_mapping" && private.request.is_none());
    for mode in ["model-forbidden", "model-unavailable"] {
        set_policy(mode);
        let refused = lookup.resolve(reference("weights")).unwrap();
        assert!(refused.outcome == mode.trim_start_matches("model-") && refused.request.is_none(), "{mode}");
    }
    set_policy("permit");
    assert_eq!(lookup.resolve(reference("weights")).unwrap().outcome, "resolved");
    println!("PASS model lookup resolved with imported request records, unsupported_mapping, forbidden and unavailable");
}

fn main() {
    let a: Vec<String> = std::env::args().collect();
    if a.len() == 2 && a[1] == "--help" {
        println!("capabilities-consumer <runtime-endpoint> all <policy-mode-file>: resolve storage changes and router through the runtime and check their refusals");
        return;
    }
    assert!(a.len() == 4 && a[2] == "all", "usage: capabilities-consumer <runtime-endpoint> all <policy-mode-file>");
    assert_eq!(sha256(b"abc"), "sha256:ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad");
    let machine = Machine::new(&a[1]);
    let path = a[3].clone();
    let set_policy = move |mode: &str| std::fs::write(&path, mode).unwrap();
    storage_changes(&machine, &set_policy);
    router_calls(&machine, &set_policy);
    model_lookup(&machine, &set_policy);
    log_observer(&machine, &a[1], &set_policy);
    rights_administration(&machine, &set_policy);
}
