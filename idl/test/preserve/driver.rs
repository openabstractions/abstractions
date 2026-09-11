#[path="generated/rs/rec.rs"] mod rec;
fn main() {
    std::panic::set_hook(Box::new(|_|{}));
    let args:Vec<_>=std::env::args().collect();
    let input=std::fs::read(&args[1]).unwrap();
    let mut r=match rec::decode(&input){Ok(r)=>r,Err(e)=>{print!("{}",e.word);return;}};
    let scope=&args[3];
    if scope=="mutate" {r.id="edited".into();if let Some(child)=&mut r.child {child.name="edited-child".into();}for child in &mut r.children {child.name="edited-child".into();}}
    else if scope!="read" {
        let target=if scope=="child" {&mut r.child.as_mut().unwrap().extras}else if scope=="repeated" {&mut r.children[0].extras}else{&mut r.extras};
        target.insert(args[4].clone(),std::fs::read(&args[5]).unwrap());
    }
    match std::panic::catch_unwind(||rec::encode(&r)) {
        Ok(output)=>{rec::decode(&output).unwrap();std::fs::write(&args[2],output).unwrap();print!("ok");},
        Err(e)=>{
            let text=e.downcast_ref::<String>().map(String::as_str).or_else(||e.downcast_ref::<&str>().copied()).unwrap_or("");
            for word in ["duplicate_field","duplicate_key","depth_exceeded","bad_string","trailing_bytes","malformed","number_spelling","wrong_type"] {
                if text.contains(word){print!("{}",word);return;}
            }
            std::panic::resume_unwind(e);
        }
    }
}
