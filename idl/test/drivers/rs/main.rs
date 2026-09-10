mod rec;

use std::collections::BTreeMap;
use std::fs;

const TS: &str = "2026-09-08T05:07:14.951609Z";

fn cp(points: &[u32]) -> String {
    points.iter().map(|c| char::from_u32(*c).unwrap()).collect()
}

fn awkward() -> rec::Record {
    let by = format!(
        "Ada L{}velace <ada@{}.jp> & co {} {}",
        cp(&[0x016B]),
        cp(&[0x4F8B, 0x3048]),
        cp(&[0x2702, 0xFE0F]),
        cp(&[0x1F9FF])
    );
    let err = format!(
        "line1{}line2{}tabbed{}ctrl {}quoted{} back{}slash",
        cp(&[0x0A]),
        cp(&[0x09]),
        cp(&[0x01]),
        cp(&[0x22]),
        cp(&[0x22]),
        cp(&[0x5C])
    );
    let mut extensions = BTreeMap::new();
    extensions.insert("zz.example/v1".into(), r#"{"k":"v"}"#.into());
    extensions.insert("aa.example/v1".into(), "[1,2,3]".into());
    extensions.insert(cp(&[0x00E9]) + ".example", "null".into());
    extensions.insert(cp(&[0xFFFD]) + ".example", "true".into());
    extensions.insert(cp(&[0x1D11E]) + ".example", "{}".into());
    rec::Record {
        content: vec![
            "abstraction.job/base@1".into(),
            "abstraction.job/intent@1".into(),
            "abstraction.job/envelope@1".into(),
            "abstraction.job/step@1".into(),
        ],
        critical: vec!["abstraction.job/base@1".into()],
        id: "1787202430967-a752f9a9c2c77b123ffd".into(),
        kind: "download".into(),
        envelope: Some(rec::Envelope {
            schema: "nas.example/transfer@2".into(),
            actions: vec![
                "cancel".into(),
                "nas.example/transfer@2#re-mirror".into(),
            ],
        }),
        state: "pending".into(),
        spec: concat!(
            r#"{"artifact":{"bytes":9223372036854775807,"empty_obj":{},"empty_arr":[]},"#,
            r#""note":"a<b&c>d","nested":{"deep":{"x":1.50,"neg":-0.0}}}"#
        )
        .into(),
        progress: rec::Progress {
            total: 9223372036854775807,
            updated_at: TS.into(),
            step: Some(rec::Step {
                ordinal: 1,
                total: i64::MIN,
                ..Default::default()
            }),
            ..Default::default()
        },
        lease: rec::Lease {
            expires_at: TS.into(),
            ..Default::default()
        },
        error: err,
        intent: Some(rec::Intent {
            want: "cancel".into(),
            by,
            at: TS.into(),
        }),
        extensions,
        created_at: TS.into(),
        updated_at: TS.into(),
        ..Default::default()
    }
}

fn ranges() -> rec::Record {
    rec::Record {
        content: vec![
            "abstraction.job/base@1".into(),
            "abstraction.download/ranges@1".into(),
        ],
        critical: vec!["abstraction.job/base@1".into()],
        id: "1787202430967-a752f9a9c2c77b123ffd".into(),
        kind: "download".into(),
        state: "running".into(),
        spec: r#"{"artifact":{"bytes":23068672}}"#.into(),
        checkpoint: concat!(
            r#"{"verified_prefix":4194304,"verified":"#,
            r#"[[0,4194304],[8388608,12582912],[20971520,23068672]]}"#
        )
        .into(),
        progress: rec::Progress {
            done: 10485760,
            total: 23068672,
            updated_at: "2026-08-20T05:07:14.951609Z".into(),
            step: None,
        },
        lease: rec::Lease {
            owner: "go-worker".into(),
            epoch: 2,
            expires_at: "2026-08-20T05:08:14.635068Z".into(),
            recall: None,
        },
        created_at: "2026-08-20T05:07:10.967343Z".into(),
        updated_at: "2026-08-20T05:07:15.134811Z".into(),
        ..Default::default()
    }
}

fn terminal() -> rec::Record {
    rec::Record {
        content: vec![
            "abstraction.job/base@1".into(),
            "abstraction.job/step@1".into(),
            "abstraction.job/terminal@1".into(),
            "abstraction.job/recall@1".into(),
        ],
        critical: vec![
            "abstraction.job/base@1".into(),
            "abstraction.job/terminal@1".into(),
            "abstraction.job/recall@1".into(),
        ],
        id: "1787202430967-a752f9a9c2c77b123ffd".into(),
        kind: "download".into(),
        state: "failed".into(),
        spec: r#"{"artifact":{"bytes":64}}"#.into(),
        checkpoint: r#"{"verified_prefix":8}"#.into(),
        progress: rec::Progress {
            done: 8,
            total: 64,
            updated_at: "2026-09-09T17:21:08.958178Z".into(),
            step: Some(rec::Step {
                name: "fetch".into(),
                ordinal: 1,
                of: 2,
                done: 8,
                total: 64,
            }),
        },
        lease: rec::Lease {
            owner: "alpha".into(),
            epoch: 3,
            expires_at: "2026-09-09T17:21:09.958178Z".into(),
            recall: Some(rec::Recall {
                reason: "yield".into(),
                by: "broker".into(),
                at: "2026-09-09T17:21:08.958178Z".into(),
                until: "2026-09-09T17:21:09.958178Z".into(),
            }),
        },
        error: "source closed the connection".into(),
        created_at: "2026-09-09T17:21:06.998457Z".into(),
        updated_at: "2026-09-09T17:21:08.967883Z".into(),
        ..Default::default()
    }
}

fn verdict(data: &[u8]) -> String {
    match rec::decode(data) {
        Ok(v) => format!("ok\t{}", String::from_utf8_lossy(&v.spec)),
        Err(r) => format!("{}\t{}", r.word, r.offset),
    }
}

fn main() {
    let mut args = std::env::args().skip(1);
    let dir = args.next().expect("usage: main <outdir> <corpusdir>");
    let corpus = args.next().expect("usage: main <outdir> <corpusdir>");
    for (name, r) in [("awkward", awkward()), ("ranges", ranges()), ("terminal", terminal())] {
        let encoded = rec::encode(&r);
        fs::write(format!("{dir}/rs-{name}.json"), &encoded).unwrap();
        let back = rec::decode(&encoded).unwrap_or_else(|e| panic!("{name}: {e}"));
        fs::write(format!("{dir}/rs-rt-{name}.json"), rec::encode(&back)).unwrap();
    }
    let mut names: Vec<_> = fs::read_dir(&corpus)
        .unwrap()
        .filter_map(|e| e.ok().map(|e| e.file_name().to_string_lossy().into_owned()))
        .filter(|n| n.ends_with(".json"))
        .collect();
    names.sort();
    let mut out = String::new();
    for n in &names {
        let data = fs::read(format!("{corpus}/{n}")).unwrap();
        out.push_str(&format!("{}\t{}\n", &n[..n.len() - 5], verdict(&data)));
    }
    fs::write(format!("{dir}/rs-corpus.txt"), out).unwrap();
}
