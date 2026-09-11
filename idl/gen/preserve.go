package main

import (
	"strconv"
	"strings"
)

func quotedFieldNames(st Struct) string {
	var names []string
	for _, f := range st.Fields {
		names = append(names, strconv.Quote(f.Name))
	}
	return strings.Join(names, ", ")
}

// Extra values go through the existing grammar scanner, at their enclosing
// object's actual depth. Validation must precede raw reflow, which assumes
// valid JSON. None of these helpers changes the grant/drop path.
const goPreserve = `
func extraFields(out []byte, extras map[string]Raw, known []string, depth int, first bool) ([]byte, bool) {
 for _, key := range sortedKeys(extras) {
  for _, name := range known { if key == name { panic(&Refusal{Word:"duplicate_field"}) } }
  // Minimal escaping preserves invalid UTF-8 for the reader to refuse rather
  // than letting an ASCII encoder replace it before validation.
  keyBytes := []byte{'"'}
  for i:=0;i<len(key);i++ { keyBytes=escByte(keyBytes,key[i]) }; keyBytes=append(keyBytes,'"')
  kr:=reader{buf:keyBytes}; if _,err:=kr.str();err!=nil { panic(err) }
  value:=extras[key]
  r:=reader{buf:[]byte(value),depth:depth}
  if err:=r.enter();err!=nil { panic(err) }; r.ws()
  if err:=r.skipValue();err!=nil { panic(err) }; r.ws()
  if r.pos!=len(r.buf) { panic(r.refuse("trailing_bytes")) }
  if !first { out=append(out,',') }; first=false
  out=append(out,'\n'); out=pad(out,depth+1); out=esc(out,key);out=append(out,':',' ')
  out=raw(out,value,depth+1)
 }
 return out,first
}
`

const pyPreserve = `
def _extra_fields(out, extras, known, depth, first):
    if not isinstance(extras, dict) or any(not isinstance(k, str) for k in extras):
        raise Refusal("wrong_type", 0)
    try:
        keys = sorted(extras, key=lambda k: k.encode("utf-8"))
    except UnicodeError:
        raise Refusal("bad_string", 0) from None
    for key in keys:
        if key in known:
            raise Refusal("duplicate_field", 0)
        value = extras[key]
        if not isinstance(value, (str, bytes, bytearray)):
            raise Refusal("wrong_type", 0)
        try:
            value = value.encode("utf-8") if isinstance(value, str) else bytes(value)
        except UnicodeError:
            raise Refusal("bad_string", 0) from None
        r = _Reader(value)
        r.depth = depth
        r.enter()
        r.ws()
        r.skip_value()
        r.ws()
        if r.pos != len(r.buf):
            raise r.refuse("trailing_bytes")
        if not first:
            out += b","
        first = False
        out += b"\n"
        pad(out, depth + 1)
        esc(out, key)
        out += b": "
        raw(out, value, depth + 1)
    return first
`

const jsPreserve = `
function extraText(s) {
  // TextEncoder would otherwise replace an unpaired UTF-16 surrogate.
  for(const ch of s) {
    const cp=ch.codePointAt(0);
    if(cp>=0xd800 && cp<=0xdfff) throw new Refusal("bad_string",0);
  }
  return ENC.encode(s);
}
function extraFields(out, extras, known, depth, first) {
  if(extras===null || typeof extras!=="object" || Array.isArray(extras)) throw new Refusal("wrong_type",0);
  const keys=Object.keys(extras);
  for(const key of keys) extraText(key);
  keys.sort(byteLess);
  for(const key of keys) {
    if(known.includes(key)) throw new Refusal("duplicate_field",0);
    let value=extras[key];
    if(typeof value==="string") value=extraText(value);
    else if(!(value instanceof Uint8Array)) throw new Refusal("wrong_type",0);
    const r=new Reader(value); r.depth=depth; r.enter(); r.ws(); r.skipValue(); r.ws();
    if(r.pos!==r.buf.length) throw r.refuse("trailing_bytes");
    if(!first) out.byte(0x2c); first=false;
    out.byte(0x0a); pad(out,depth+1); esc(out,key); out.ascii(": "); raw(out,value,depth+1);
  }
  return first;
}
`

const cppPreserve = `
inline bool extra_fields(std::string& out, const std::map<std::string, Raw>& extras,
                         const std::vector<std::string>& known, int depth, bool first) {
    for(const auto& entry : extras) {
        const auto& key=entry.first;
        for(const auto& name : known) if(key==name) throw Refusal("duplicate_field",0);
        std::string key_bytes="\"";
        for(unsigned char c : key) esc_byte(key_bytes,c);
        key_bytes+='"';
        Reader kr{key_bytes}; kr.str();
        const auto& value=entry.second;
        Reader r{value}; r.depth=depth; r.enter(); r.skip_ws(); r.skip_value(); r.skip_ws();
        if(r.pos!=r.buf.size()) r.refuse("trailing_bytes");
        if(!first) out+=','; first=false;
        out+='\n'; pad(out,depth+1); esc(out,key); out+=": "; raw(out,value,depth+1);
    }
    return first;
}
`

const rsPreserve = `
fn extra_fields(out: &mut Vec<u8>, extras: &BTreeMap<String, Raw>, known: &[&str], depth: i32, mut first: bool) -> bool {
    for (key,value) in extras {
        assert!(!known.contains(&key.as_str()), "duplicate_field");
        let mut r=Reader{buf:value,pos:0,depth};
        r.enter().unwrap(); r.skip_ws(); r.skip_value().unwrap(); r.skip_ws();
        if r.pos!=r.buf.len() {r.refuse::<()>("trailing_bytes").unwrap();}
        if !first {out.push(b',');} first=false;
        out.push(b'\n');pad(out,depth+1);esc(out,key);out.extend_from_slice(b": ");raw(out,value,depth+1);
    }
    first
}
`
