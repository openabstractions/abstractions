mod rec;

use std::collections::BTreeMap;
use std::fs;
use std::io::Write;
use std::path::{Path, PathBuf};
use std::time::{Duration, Instant, SystemTime, UNIX_EPOCH};

const KIND: &str = "download";
const ME: &str = "rust-replay";
const TERMINAL: [&str; 3] = ["complete", "failed", "cancelled"];
const WANTS: [&str; 3] = ["run", "pause", "cancel"];
const FINISHES: [&str; 4] = ["transferred", "complete", "failed", "cancelled"];
const BASE: &str = "abstraction.job/base@1";
const INTENT: &str = "abstraction.job/intent@1";
const RANGES: &str = "abstraction.download/ranges@1";
const DELEGATION: &str = "abstraction.job/delegation@1";
const ENVELOPE: &str = "abstraction.job/envelope@1";
const STEP: &str = "abstraction.job/step@1";
const RECALL: &str = "abstraction.job/recall@1";
const TERMINAL_MODEL: &str = "abstraction.job/terminal@1";

fn now_us() -> i64 {
    SystemTime::now().duration_since(UNIX_EPOCH).unwrap().as_micros() as i64
}

fn civil_from_days(z: i64) -> (i64, u32, u32) {
    let z = z + 719_468;
    let era = z.div_euclid(146_097);
    let doe = z.rem_euclid(146_097);
    let yoe = (doe - doe / 1460 + doe / 36_524 - doe / 146_096) / 365;
    let doy = doe - (365 * yoe + yoe / 4 - yoe / 100);
    let mp = (5 * doy + 2) / 153;
    let d = (doy - (153 * mp + 2) / 5 + 1) as u32;
    let m = if mp < 10 { mp + 3 } else { mp - 9 } as u32;
    let y = yoe + era * 400 + if m <= 2 { 1 } else { 0 };
    (y, m, d)
}

fn days_from_civil(y: i64, m: u32, d: u32) -> i64 {
    let y = if m <= 2 { y - 1 } else { y };
    let era = y.div_euclid(400);
    let yoe = y.rem_euclid(400);
    let mp = if m > 2 { m - 3 } else { m + 9 } as i64;
    let doy = (153 * mp + 2) / 5 + d as i64 - 1;
    era * 146_097 + yoe * 365 + yoe / 4 - yoe / 100 + doy - 719_468
}

fn stamp(us: i64) -> String {
    let secs = us.div_euclid(1_000_000);
    let (y, m, d) = civil_from_days(secs.div_euclid(86_400));
    let sod = secs.rem_euclid(86_400);
    format!(
        "{:04}-{:02}-{:02}T{:02}:{:02}:{:02}.{:06}Z",
        y, m, d, sod / 3600, sod % 3600 / 60, sod % 60, us.rem_euclid(1_000_000)
    )
}

fn parse_stamp(s: &str) -> Option<i64> {
    if !rec::wide_timestamp(s) {
        return None;
    }
    let b = s.as_bytes();
    let num = |i: usize, n: usize| -> i64 { s[i..i + n].parse().unwrap() };
    let days = days_from_civil(num(0, 4), num(5, 2) as u32, num(8, 2) as u32);
    let secs = days * 86_400 + num(11, 2) * 3600 + num(14, 2) * 60 + num(17, 2);
    let mut us = secs * 1_000_000;
    let mut i = 19;
    if b[i] == b'.' {
        i += 1;
        let mut scale = 1_000_000;
        while b[i].is_ascii_digit() {
            if scale > 1 {
                scale /= 10;
                us += (b[i] - b'0') as i64 * scale;
            }
            i += 1;
        }
    }
    if b[i] == b'+' || b[i] == b'-' {
        let offset = (num(i + 1, 2) * 3600 + num(i + 4, 2) * 60) * 1_000_000;
        us += if b[i] == b'+' { -offset } else { offset };
    }
    Some(us)
}

fn live(lease: &rec::Lease, now: i64) -> bool {
    !lease.owner.is_empty() && parse_stamp(&lease.expires_at).map_or(false, |e| e > now)
}

fn terminal(r: &rec::Record) -> bool {
    TERMINAL.contains(&r.state.as_str())
}

/// [JOB-D2] both lists are derived from what the record carries on every write,
/// in the order the contract's table names the models, extension keys last and
/// sorted [JOB-D3]. `ranges@1` covers `checkpoint.verified` [JOB-C2], and
/// `rec::member` is how a store that never reads a checkpoint [JOB-K1] can
/// still say whether one names that key.
fn declare(r: &mut rec::Record) {
    let mut content = vec![BASE.to_string()];
    let mut critical = vec![BASE.to_string()];
    let mut add = |name: &str, crit: bool| {
        content.push(name.to_string());
        if crit {
            critical.push(name.to_string());
        }
    };
    if r.intent.is_some() {
        add(INTENT, true);
    }
    if r.delegation.is_some() {
        add(DELEGATION, true);
    }
    if r.progress.step.is_some() {
        add(STEP, false);
    }
    if rec::member(&r.checkpoint, "verified") {
        add(RANGES, false);
    }
    if terminal(r) {
        add(TERMINAL_MODEL, true);
    }
    if r.lease.recall.is_some() {
        add(RECALL, true);
    }
    if r.envelope.is_some() {
        add(ENVELOPE, true);
    }
    for k in r.extensions.keys() {
        content.push(k.clone());
    }
    r.content = content;
    r.critical = critical;
}

struct Store {
    jobs: PathBuf,
}

impl Store {
    fn open(root: &Path) -> Store {
        let jobs = root.join("jobs");
        fs::create_dir_all(&jobs).expect("jobs/");
        fs::create_dir_all(root.join("work")).expect("work/");
        Store { jobs }
    }

    fn path(&self, id: &str) -> PathBuf {
        self.jobs.join(format!("{id}.json"))
    }

    fn load(&self, id: &str) -> Result<rec::Record, &'static str> {
        let data = fs::read(self.path(id)).map_err(|_| "not-found")?;
        match rec::decode(&data) {
            Ok(r) => Ok(r),
            Err(e) if e.word == "unknown_critical" => Err("unknown-model"),
            Err(_) => Err("invalid"),
        }
    }

    fn write_raw(&self, id: &str, bytes: &[u8]) {
        let tmp = self.jobs.join(format!("{id}.json.{}.tmp", std::process::id()));
        fs::write(&tmp, bytes).expect("write tmp");
        fs::rename(&tmp, self.path(id)).expect("rename over record");
    }

    fn save(&self, id: &str, r: &mut rec::Record) -> Result<(), &'static str> {
        declare(r);
        r.updated_at = stamp(now_us());
        let bytes = rec::encode(r);
        if rec::decode(&bytes).is_err() {
            return Err("invalid");
        }
        self.write_raw(id, &bytes);
        Ok(())
    }
}

#[cfg(windows)]
mod power {
    #[repr(C)]
    struct ReasonContext {
        version: u32,
        flags: u32,
        simple: *const u16,
        detailed_rest: [usize; 3],
    }

    #[link(name = "kernel32")]
    extern "system" {
        fn PowerCreateRequest(context: *const ReasonContext) -> isize;
        fn PowerSetRequest(request: isize, kind: i32) -> i32;
        fn PowerClearRequest(request: isize, kind: i32) -> i32;
        fn CloseHandle(handle: isize) -> i32;
    }

    const SIMPLE_STRING: u32 = 1;
    const SYSTEM_REQUIRED: i32 = 1;
    const INVALID: isize = -1;

    pub struct Hold(isize);

    pub fn acquire(reason: &str) -> Hold {
        let wide: Vec<u16> = reason.encode_utf16().chain(std::iter::once(0)).collect();
        let ctx = ReasonContext { version: 0, flags: SIMPLE_STRING, simple: wide.as_ptr(), detailed_rest: [0; 3] };
        let h = unsafe { PowerCreateRequest(&ctx) };
        if h != INVALID && h != 0 {
            unsafe { PowerSetRequest(h, SYSTEM_REQUIRED) };
        }
        Hold(h)
    }

    impl Drop for Hold {
        fn drop(&mut self) {
            if self.0 != INVALID && self.0 != 0 {
                unsafe {
                    PowerClearRequest(self.0, SYSTEM_REQUIRED);
                    CloseHandle(self.0);
                }
            }
        }
    }
}

#[cfg(not(windows))]
mod power {
    pub struct Hold;
    pub fn acquire(_reason: &str) -> Hold {
        Hold
    }
}

struct Held {
    epoch: i64,
    _os: power::Hold,
}

struct Listener {
    budget: Option<Duration>,
    last: Option<Vec<String>>,
    since: Instant,
    closed: bool,
}

struct Driver {
    store: Store,
    work: PathBuf,
    ids: BTreeMap<String, String>,
    epochs: BTreeMap<String, i64>,
    holds: BTreeMap<String, Held>,
    listeners: BTreeMap<String, Listener>,
    rng: u64,
    seq: u32,
}

impl Driver {
    fn new(work: &Path) -> Driver {
        Driver {
            store: Store::open(work),
            work: work.to_path_buf(),
            ids: BTreeMap::new(),
            epochs: BTreeMap::new(),
            holds: BTreeMap::new(),
            listeners: BTreeMap::new(),
            rng: now_us() as u64 | 1,
            seq: 0,
        }
    }

    fn mint(&mut self) -> String {
        self.rng ^= self.rng << 13;
        self.rng ^= self.rng >> 7;
        self.rng ^= self.rng << 17;
        self.seq += 1;
        format!("{}-{:016x}{:04x}", now_us() / 1000, self.rng, self.seq)
    }

    fn record(&self, alias: &str) -> Result<(String, rec::Record), &'static str> {
        let id = self.ids.get(alias).ok_or("not-found")?;
        Ok((id.clone(), self.store.load(id)?))
    }

    fn reconcile(&mut self) {
        let now = now_us();
        let ended: Vec<String> = self
            .holds
            .iter()
            .filter(|(alias, h)| {
                let r = self.ids.get(*alias).and_then(|id| self.store.load(id).ok());
                !r.map_or(false, |r| r.lease.epoch == h.epoch && live(&r.lease, now) && !terminal(&r))
            })
            .map(|(alias, _)| alias.clone())
            .collect();
        for alias in ended {
            self.holds.remove(&alias);
        }
    }

    fn show(&mut self, verdict: &str, alias: &str, r: &rec::Record) -> String {
        self.reconcile();
        let now = now_us();
        let yes_no = |b: bool| if b { "yes" } else { "no" };
        let mut cp = Vec::new();
        if r.checkpoint.is_empty() {
            cp.extend_from_slice(b"none");
        } else {
            rec::raw_flat(&mut cp, &r.checkpoint);
        }
        format!(
            "{} state={} epoch={} held={} recall={} want={} done={} err={} cp={} content={} crit={} awake={}",
            verdict,
            r.state,
            r.lease.epoch,
            yes_no(live(&r.lease, now)),
            r.lease.recall.as_ref().map_or("none", |c| c.reason.as_str()),
            r.intent.as_ref().map_or("run", |i| i.want.as_str()),
            r.progress.done,
            if r.error.is_empty() { "none" } else { "set" },
            String::from_utf8_lossy(&cp),
            r.content.join(","),
            r.critical.join(","),
            yes_no(self.holds.get(alias).map_or(false, |h| h.epoch == r.lease.epoch)),
        )
    }

    fn write(
        &mut self,
        alias: &str,
        owner: Option<&str>,
        on_terminal: bool,
        change: impl FnOnce(&mut rec::Record, i64) -> Result<(), &'static str>,
    ) -> String {
        let (id, mut r) = match self.record(alias) {
            Ok(x) => x,
            Err(v) => return v.to_string(),
        };
        if !on_terminal && terminal(&r) {
            return self.show("terminal", alias, &r);
        }
        let now = now_us();
        if let Some(o) = owner {
            if self.epochs.get(o).copied().unwrap_or(0) != r.lease.epoch {
                return self.show("stale-epoch", alias, &r);
            }
            if !live(&r.lease, now) {
                return self.show("lease-expired", alias, &r);
            }
        }
        match change(&mut r, now).and_then(|_| self.store.save(&id, &mut r)) {
            Ok(()) => self.show("ok", alias, &r),
            Err(v) => match self.store.load(&id) {
                Ok(r) => self.show(v, alias, &r),
                Err(_) => v.to_string(),
            },
        }
    }

    fn submit(&mut self, alias: &str, keys: &[&str]) -> String {
        let mut size: i64 = 0;
        let mut src = "file";
        for k in keys {
            match k.split_once('=') {
                Some(("size", v)) => size = v.parse().unwrap_or(0),
                Some(("src", v)) => src = v,
                _ => {}
            }
        }
        let slash = |p: PathBuf| p.to_string_lossy().replace('\\', "/");
        let source = slash(self.work.join("src").join(alias));
        if src == "file" {
            fs::create_dir_all(self.work.join("src")).expect("src/");
            let bytes: Vec<u8> = (0..size).map(|i| (i % 251) as u8).collect();
            fs::write(&source, bytes).expect("source file");
        }
        let sink = slash(self.work.join("out").join(alias));
        let now = now_us();
        let id = self.mint();
        let mut r = rec::Record {
            id: id.clone(),
            kind: KIND.into(),
            state: "pending".into(),
            spec: format!(
                r#"{{"artifact":{{"bytes":{size}}},"sources":[{{"url":"file:///{source}"}}],"sink":{{"path":"{sink}"}}}}"#
            )
            .into_bytes(),
            progress: rec::Progress { updated_at: stamp(now), ..Default::default() },
            lease: rec::Lease { expires_at: stamp(now), ..Default::default() },
            created_at: stamp(now),
            ..Default::default()
        };
        self.ids.insert(alias.to_string(), id.clone());
        match self.store.save(&id, &mut r) {
            Ok(()) => self.show("ok", alias, &r),
            Err(v) => v.to_string(),
        }
    }

    fn claim(&mut self, alias: &str, owner: &str, ttl_ms: i64) -> String {
        let (id, mut r) = match self.record(alias) {
            Ok(x) => x,
            Err(v) => return v.to_string(),
        };
        if terminal(&r) {
            return self.show("terminal", alias, &r);
        }
        let now = now_us();
        if live(&r.lease, now) {
            return self.show("lease-held", alias, &r);
        }
        r.lease.epoch += 1;
        r.lease.owner = owner.to_string();
        r.lease.expires_at = stamp(now + ttl_ms * 1000);
        r.lease.recall = None;
        if r.state == "pending" {
            r.state = "running".into();
        }
        self.epochs.insert(owner.to_string(), r.lease.epoch);
        match self.store.save(&id, &mut r) {
            Ok(()) => self.show("ok", alias, &r),
            Err(v) => v.to_string(),
        }
    }

    fn hold(&mut self, alias: &str) -> String {
        let (_, r) = match self.record(alias) {
            Ok(x) => x,
            Err(v) => return v.to_string(),
        };
        if live(&r.lease, now_us()) && !terminal(&r) {
            let os = power::acquire(&format!("{ME}: {} epoch {}", r.id, r.lease.epoch));
            self.holds.insert(alias.to_string(), Held { epoch: r.lease.epoch, _os: os });
        }
        self.show("ok", alias, &r)
    }

    /// A transferred record is left out although no rule says so: orphans.txt
    /// names the cost, "a transferred job re-fetched forever", and Go's driver
    /// leaves it out. [JOB-I11] and [JOB-I12] are the pause rows.
    fn orphans(&mut self) -> String {
        let now = now_us();
        let names: Vec<String> = self
            .ids
            .iter()
            .filter(|(_, id)| match self.store.load(id) {
                Ok(r) => {
                    let paused = r.intent.as_ref().map_or(false, |i| i.want == "pause");
                    !terminal(&r)
                        && r.state != "transferred"
                        && !live(&r.lease, now)
                        && (!paused || r.state == "running")
                }
                Err(_) => false,
            })
            .map(|(alias, _)| alias.clone())
            .collect();
        self.reconcile();
        if names.is_empty() {
            "ok -".to_string()
        } else {
            format!("ok {}", names.join(" "))
        }
    }

    fn plant(&mut self, alias: &str, list: &str, name: &str) -> String {
        let (id, mut r) = match self.record(alias) {
            Ok(x) => x,
            Err(v) => return v.to_string(),
        };
        match list {
            "content" => r.content.push(name.to_string()),
            "critical" => {
                r.content.push(name.to_string());
                r.critical.push(name.to_string());
            }
            _ => return "invalid".to_string(),
        }
        self.store.write_raw(&id, &rec::encode(&r));
        match self.store.load(&id) {
            Ok(r) => self.show("ok", alias, &r),
            Err(v) => v.to_string(),
        }
    }

    fn snapshot(&self) -> (Vec<String>, String) {
        let mut keys = Vec::new();
        let mut shown = Vec::new();
        for (alias, id) in &self.ids {
            if let Ok(r) = self.store.load(id) {
                let p = &r.progress;
                keys.push(format!("{alias}\t{id}\t{}\t{}\t{}\t{}\t{}", r.state, p.done, p.total, r.lease.owner, r.error));
                shown.push(format!("{alias}={}/{}", r.state, p.done));
            }
        }
        let shown = if shown.is_empty() { "-".to_string() } else { shown.join(" ") };
        (keys, shown)
    }

    fn next(&mut self, name: &str) -> String {
        let (keys, shown) = self.snapshot();
        let l = match self.listeners.get_mut(name) {
            Some(l) => l,
            None => return "not-found".to_string(),
        };
        if l.closed {
            return "closed".to_string();
        }
        if l.last.as_ref() != Some(&keys) {
            l.last = Some(keys);
            l.since = Instant::now();
            return format!("changed {shown}");
        }
        let budget = match l.budget {
            Some(b) => b,
            None => return "refused".to_string(),
        };
        let remaining = budget.saturating_sub(l.since.elapsed());
        std::thread::sleep(remaining);
        let (keys, shown) = self.snapshot();
        let l = self.listeners.get_mut(name).unwrap();
        if l.last.as_ref() != Some(&keys) {
            l.last = Some(keys);
            l.since = Instant::now();
            return format!("changed {shown}");
        }
        l.since = Instant::now();
        format!("quiet {shown}")
    }

    fn apply(&mut self, line: &str) -> String {
        let words: Vec<&str> = line.split_whitespace().collect();
        let arg = |i: usize| words.get(i).copied().unwrap_or("");
        let int = |i: usize| arg(i).parse::<i64>().ok();
        let rest = |n: usize| -> String { words.iter().skip(n).copied().collect::<Vec<_>>().join(" ") };
        match words[0] {
            "submit" => self.submit(arg(1), &words[2..]),
            "claim" => match int(3) {
                Some(ttl) => self.claim(arg(1), arg(2), ttl),
                None => "invalid".to_string(),
            },
            "renew" => {
                let ttl = int(3);
                self.write(arg(1), Some(arg(2)), true, |r, now| {
                    let mut expires = now + ttl.ok_or("invalid")? * 1000;
                    if let Some(until) = r.lease.recall.as_ref().and_then(|c| parse_stamp(&c.until)) {
                        expires = expires.min(until);
                    }
                    r.lease.expires_at = stamp(expires);
                    Ok(())
                })
            }
            "progress" => {
                let done = int(3);
                let checkpoint = rest(4);
                self.write(arg(1), Some(arg(2)), false, |r, now| {
                    r.progress.done = done.ok_or("invalid")?;
                    r.progress.updated_at = stamp(now);
                    if !checkpoint.is_empty() {
                        r.checkpoint = checkpoint.into_bytes();
                    }
                    Ok(())
                })
            }
            "release" => self.write(arg(1), Some(arg(2)), false, |r, now| {
                r.lease.owner.clear();
                r.lease.expires_at = stamp(now);
                if r.state == "running" {
                    r.state = "pending".into();
                }
                Ok(())
            }),
            "finish" => {
                let state = arg(3).to_string();
                self.write(arg(1), Some(arg(2)), false, |r, _| {
                    if !FINISHES.contains(&state.as_str()) {
                        return Err("invalid");
                    }
                    r.state = state;
                    Ok(())
                })
            }
            "intent" => {
                let want = arg(2).to_string();
                self.write(arg(1), None, false, |r, now| {
                    if !WANTS.contains(&want.as_str()) {
                        return Err("invalid");
                    }
                    r.intent = Some(rec::Intent { want, by: ME.into(), at: stamp(now) });
                    Ok(())
                })
            }
            "recall" => {
                let grace = int(3);
                let reason = rest(4);
                self.write(arg(1), Some(arg(2)), false, |r, now| {
                    if reason.is_empty() {
                        return Err("invalid");
                    }
                    let until = now + grace.ok_or("invalid")? * 1000;
                    r.lease.recall = Some(rec::Recall { reason, by: ME.into(), at: stamp(now), until: stamp(until) });
                    if parse_stamp(&r.lease.expires_at).map_or(true, |e| until < e) {
                        r.lease.expires_at = stamp(until);
                    }
                    Ok(())
                })
            }
            "hold" => self.hold(arg(1)),
            "state" => match self.record(arg(1)) {
                Ok((_, r)) => self.show("ok", arg(1), &r),
                Err(v) => v.to_string(),
            },
            "orphans" => self.orphans(),
            "plant" => self.plant(arg(1), arg(2), arg(3)),
            "sleep" => {
                std::thread::sleep(Duration::from_millis(int(1).unwrap_or(0) as u64));
                "ok".to_string()
            }
            "watch" => {
                let budget = int(2).map(|ms| Duration::from_millis(ms as u64));
                self.listeners.insert(
                    arg(1).to_string(),
                    Listener { budget, last: None, since: Instant::now(), closed: false },
                );
                "ok".to_string()
            }
            "next" => self.next(arg(1)),
            "close" => match self.listeners.get_mut(arg(1)) {
                Some(l) => {
                    l.closed = true;
                    "ok".to_string()
                }
                None => "not-found".to_string(),
            },
            _ => "unknown-op".to_string(),
        }
    }
}

fn models() -> String {
    let mut out = String::new();
    for name in rec::CONTENT_TERMS {
        let mark = if rec::CONTENT_STRIP_CRITICAL.contains(&name) { "never-critical" } else { "critical-ok" };
        out.push_str(&format!("{name} {mark}\n"));
    }
    out
}

fn run(work: &Path, scenario: &Path) {
    let text = fs::read_to_string(scenario).expect("scenario file");
    let mut driver = Driver::new(work);
    let mut out = std::io::stdout().lock();
    let mut n = 0;
    for line in text.lines() {
        let line = line.trim_end_matches('\r');
        if line.trim().is_empty() || line.starts_with('#') {
            continue;
        }
        n += 1;
        let answer = driver.apply(line);
        write!(out, "{n:02} {line} -> {answer}\n").expect("stdout");
    }
    out.flush().expect("stdout");
}

fn main() {
    let args: Vec<String> = std::env::args().collect();
    match (args.get(1).map(String::as_str), args.get(2)) {
        (Some("--capabilities"), None) => print!("store\n"),
        (Some("--models"), None) => print!("{}", models()),
        (Some(work), Some(scenario)) => run(Path::new(work), Path::new(scenario)),
        _ => {
            eprintln!("usage: replay --capabilities | --models | <workdir> <scenario-file>");
            std::process::exit(2);
        }
    }
}
