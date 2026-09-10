mod rec;

use std::fs;

fn main() {
    let mut args = std::env::args().skip(1);
    let dir = args.next().expect("usage: main <outdir> <corpusdir>");
    let corpus = args.next().expect("usage: main <outdir> <corpusdir>");
    let mut names: Vec<_> = fs::read_dir(&corpus)
        .unwrap()
        .filter_map(|e| e.ok().map(|e| e.file_name().to_string_lossy().into_owned()))
        .filter(|n| n.ends_with(".json"))
        .collect();
    names.sort();
    let mut out = String::new();
    for n in &names {
        let stem = &n[..n.len() - 5];
        let data = fs::read(format!("{corpus}/{n}")).unwrap();
        match rec::decode(&data) {
            Ok(v) => {
                out.push_str(&format!("{stem}\tok\n"));
                fs::write(format!("{dir}/rs-rt-{stem}.json"), rec::encode(&v)).unwrap();
            }
            Err(r) => out.push_str(&format!("{stem}\t{}\t{}\n", r.word, r.offset)),
        }
    }
    fs::write(format!("{dir}/rs-corpus.txt"), out).unwrap();
}
