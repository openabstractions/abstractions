package main

import (
	"fmt"
	"strconv"
	"strings"
)

func configCatalogueGo(b *strings.Builder, s *Definition) {
	if !configCatalogueReady(s) {
		return
	}
	fmt.Fprintf(b, "\nconst ConfigCatalogueJSON = %s\n", strconv.Quote(s.ConfigCatalogue))
	fmt.Fprintf(b, "\nfunc ValidateConfigValue(v *Value) error { if v == nil || !(%s) { return &Refusal{Word:\"bad_value\"} }; data,err:=ToJSON(v);if err!=nil{return err};var checked Value;return FromJSON(data,&checked) }\n", configValueCondition(s, "go", "v.", false))
	b.WriteString(strings.ReplaceAll(`
// ConfigKeyDescriptors returns fresh records parsed by this binding's codec.
func ConfigKeyDescriptors() []KeyDescriptor {
 r:=&reader{buf:[]byte(ConfigCatalogueJSON)};r.ws()
 rows,err:=decodeList(r,func(r *reader)(*KeyDescriptor,error){return r.decodeKeyDescriptor()})
 if err!=nil {panic(err)};r.ws();if r.pos!=len(r.buf){panic(&Refusal{Word:"trailing_bytes"})};return rows
}

// OverlayConfigProcess applies this process's values to successful entries.
// Validation precedes changes; locked and omitted/refused entries retain service authority.
func OverlayConfigProcess(entries []Entry, changes []Change) ([]Entry,error) {
 descriptors:=map[string]KeyDescriptor{};for _,d:=range ConfigKeyDescriptors(){descriptors[d.Key]=d}
 pending:=map[string]*Value{}
 for _,c:=range changes {
  d,known:=descriptors[c.Key];if !known{return nil,&Refusal{Word:"bad_value"}}
  if _,duplicate:=pending[c.Key];duplicate{return nil,&Refusal{Word:"bad_value"}}
  pending[c.Key]=nil;if c.Set==nil {continue}
  if err:=ValidateConfigValue(c.Set);err!=nil{return nil,err}
  if c.Set.Kind.String()=="unlimited" {if !d.AllowsUnlimited{return nil,&Refusal{Word:"bad_value"}}} else if c.Set.Kind!=d.Kind{return nil,&Refusal{Word:"bad_value"}}
  var n int64;numeric:=true
  switch c.Set.Kind.String(){case "integer":n=c.Set.Integer;case "bytes":n=c.Set.Bytes;case "duration":n=c.Set.DurationMs;default:numeric=false}
  if numeric&&d.Range!=nil&&(n<d.Range.Min||n>d.Range.Max){return nil,&Refusal{Word:"bad_value"}}
  if c.Set.Kind.String()=="choice" {found:=false;for _,choice:=range d.Choices{found=found||choice==c.Set.Choice};if !found{return nil,&Refusal{Word:"bad_value"}}}
  raw,err:=ToJSON(c.Set);if err!=nil{return nil,err};v:=&Value{};if err:=FromJSON(raw,v);err!=nil{return nil,err};pending[c.Key]=v
 }
 out:=make([]Entry,len(entries))
 for i,e:=range entries {
  raw,err:=ToJSON(e);if err!=nil{return nil,err};if err:=FromJSON(raw,&out[i]);err!=nil{return nil,err}
  v,has:=pending[e.Key];if !has||v==nil||string(e.Origin.State)=="locked"{continue}
  d:=descriptors[e.Key];if d.LowestLocation.String()=="process"||d.LowestLocation.String()=="user"||d.LowestLocation.String()=="machine" {
   origin:=KeyOrigin{Location:LocationProcess,State:KeyStateSet}
   if e.Origin.Location!=0&&e.Origin.Location!=LocationProcess{origin.Shadowed=append(origin.Shadowed,__CONFIG_SOURCE__{Location:e.Origin.Location,Path:e.Origin.Path})}
   origin.Shadowed=append(origin.Shadowed,e.Origin.Shadowed...);if len(origin.Shadowed)>3{origin.Shadowed=origin.Shadowed[:3]}
   out[i].Value=v;out[i].Origin=origin
  }
 }
 return out,nil
}
`, "__CONFIG_SOURCE__", configSourceName(s)))
}

func configCataloguePython(b *strings.Builder, s *Definition) {
	if !configCatalogueReady(s) {
		return
	}
	fmt.Fprintf(b, "\n\nCONFIG_CATALOGUE_JSON = %s\n", strconv.Quote(s.ConfigCatalogue))
	fmt.Fprintf(b, "\n\ndef validate_config_value(v):\n    if v is None or not (%s):\n        raise Refusal(\"bad_value\", 0)\n", configValueCondition(s, "python", "v.", false))
	b.WriteString("    from_json(Value, to_json(v))\n")
	b.WriteString(strings.ReplaceAll(`

def config_key_descriptors():
    r = _Reader(CONFIG_CATALOGUE_JSON.encode("utf-8"))
    r.ws()
    rows = _read_list(r, _read_key_descriptor)
    r.ws()
    if r.pos != len(r.buf):
        raise r.refuse("trailing_bytes")
    return rows


def overlay_config_process(entries, changes):
    descriptors = {d.key: d for d in config_key_descriptors()}
    pending = {}
    for c in changes:
        d = descriptors.get(c.key)
        if d is None or c.key in pending:
            raise Refusal("bad_value", 0)
        pending[c.key] = None
        if c.set is None:
            continue
        validate_config_value(c.set)
        if (c.set.kind == "unlimited" and not d.allows_unlimited) or (c.set.kind != "unlimited" and c.set.kind != d.kind):
            raise Refusal("bad_value", 0)
        number = {"integer": c.set.integer, "bytes": c.set.bytes, "duration": c.set.duration_ms}.get(c.set.kind)
        if number is not None and d.range is not None and not d.range.min <= number <= d.range.max:
            raise Refusal("bad_value", 0)
        if c.set.kind == "choice" and c.set.choice not in d.choices:
            raise Refusal("bad_value", 0)
        pending[c.key] = from_json(Value, to_json(c.set))
    out = [from_json(Entry, to_json(e)) for e in entries]
    for e in out:
        value = pending.get(e.key)
        if value is None or e.origin.state == "locked":
            continue
        d = descriptors[e.key]
        if d.lowest_location not in ("machine", "user", "process"):
            continue
        shadowed = list(e.origin.shadowed)
        if e.origin.location and e.origin.location != "process":
            shadowed.insert(0, __CONFIG_SOURCE__(location=e.origin.location, path=e.origin.path))
        e.value = value
        e.origin = KeyOrigin(location=Location.PROCESS, path="", state=KeyState.SET, shadowed=shadowed[:3])
    return out
`, "__CONFIG_SOURCE__", configSourceName(s)))
}

func configCatalogueJS(b *strings.Builder, s *Definition) {
	if !configCatalogueReady(s) {
		return
	}
	fmt.Fprintf(b, "\nexport const CONFIG_CATALOGUE_JSON = %s;\n", strconv.Quote(s.ConfigCatalogue))
	fmt.Fprintf(b, "\nexport function validateConfigValue(v) { if (v === null || !(%s)) throw new Refusal(\"bad_value\",0); fromJson(\"Value\",toJson(\"Value\",v)); }\n", configValueCondition(s, "javascript", "v.", false))
	b.WriteString(`
export function configKeyDescriptors() {
 const r=new Reader(new TextEncoder().encode(CONFIG_CATALOGUE_JSON));r.ws();const rows=readList(r,readKeyDescriptor);r.ws();if(r.pos!==r.buf.length)throw r.refuse("trailing_bytes");return rows;
}

export function overlayConfigProcess(entries,changes) {
 const descriptors=new Map(configKeyDescriptors().map(d=>[d.key,d]));const pending=new Map();
 for(const c of changes){
  const d=descriptors.get(c.key);if(!d||pending.has(c.key))throw new Refusal("bad_value",0);pending.set(c.key,null);if(c.set===null)continue;
  validateConfigValue(c.set);if((c.set.kind==="unlimited"&&!d.allowsUnlimited)||(c.set.kind!=="unlimited"&&c.set.kind!==d.kind))throw new Refusal("bad_value",0);
  const n={integer:c.set.integer,bytes:c.set.bytes,duration:c.set.durationMs}[c.set.kind];
  if(n!==undefined&&d.range!==null&&(n<d.range.min||n>d.range.max))throw new Refusal("bad_value",0);
  if(c.set.kind==="choice"&&!d.choices.includes(c.set.choice))throw new Refusal("bad_value",0);
  pending.set(c.key,fromJson("Value",toJson("Value",c.set)));
 }
 const out=entries.map(e=>fromJson("Entry",toJson("Entry",e)));
 for(const e of out){const value=pending.get(e.key);if(value==null||e.origin.state==="locked")continue;
  const d=descriptors.get(e.key);if(!["machine","user","process"].includes(d.lowestLocation))continue;
  const shadows=!e.origin.location||e.origin.location==="process"?[...e.origin.shadowed]:[{location:e.origin.location,path:e.origin.path},...e.origin.shadowed];
  e.value=value;e.origin={location:"process",path:"",state:"set",reason:"",shadowed:shadows.slice(0,3)};
 }return out;
}
`)
}

func configCatalogueCpp(b *strings.Builder, s *Definition) {
	if !configCatalogueReady(s) {
		return
	}
	fmt.Fprintf(b, "\ninline constexpr std::string_view kConfigCatalogueJSON = %s;\n", strconv.Quote(s.ConfigCatalogue))
	fmt.Fprintf(b, "\ninline void validate_config_value(const Value& v) { if (!(%s)) throw Refusal(\"bad_value\",0); (void)from_json<Value>(to_json(v)); }\n", configValueCondition(s, "cpp", "v.", false))
	b.WriteString(strings.ReplaceAll(`
inline std::vector<KeyDescriptor> config_key_descriptors() {
 detail::Reader r{kConfigCatalogueJSON};r.skip_ws();auto rows=detail::decode_list<KeyDescriptor>(r,detail::decode_key_descriptor);r.skip_ws();if(r.pos!=r.buf.size())r.refuse("trailing_bytes");return rows;
}
inline std::vector<Entry> overlay_config_process(const std::vector<Entry>& entries,const std::vector<Change>& changes) {
 std::map<std::string,KeyDescriptor> descriptors;for(const auto& d:config_key_descriptors())descriptors.emplace(d.key,d);
 std::map<std::string,std::optional<Value>> pending;
 for(const auto& c:changes){
  auto found=descriptors.find(c.key);if(found==descriptors.end()||pending.find(c.key)!=pending.end())throw Refusal("bad_value",0);
  const auto& d=found->second;pending.emplace(c.key,std::nullopt);if(!c.set)continue;
  validate_config_value(*c.set);if((c.set->kind=="unlimited"&&!d.allows_unlimited)||(c.set->kind!="unlimited"&&c.set->kind!=d.kind))throw Refusal("bad_value",0);
  std::int64_t n=0;bool numeric=true;
  if(c.set->kind=="integer")n=c.set->integer;else if(c.set->kind=="bytes")n=c.set->bytes;else if(c.set->kind=="duration")n=c.set->duration_ms;else numeric=false;
  if(numeric&&d.range&&(n<d.range->min||n>d.range->max))throw Refusal("bad_value",0);
  if(c.set->kind=="choice"){bool found_choice=false;for(const auto& choice:d.choices)found_choice=found_choice||choice==c.set->choice;if(!found_choice)throw Refusal("bad_value",0);}
  pending[c.key]=*c.set;
 }
 std::vector<Entry> out;out.reserve(entries.size());for(const auto& e:entries)out.push_back(from_json<Entry>(to_json(e)));
 for(auto& e:out){auto value=pending.find(e.key);if(value==pending.end()||!value->second||e.origin.state=="locked")continue;
  const auto& d=descriptors.at(e.key);if(d.lowest_location!="machine"&&d.lowest_location!="user"&&d.lowest_location!="process")continue;
  KeyOrigin origin;origin.location=Location::Process;origin.state="set";
  if(e.origin.location!=Location{}&&e.origin.location!=Location::Process){__CONFIG_SOURCE__ previous;previous.location=e.origin.location;previous.path=e.origin.path;origin.shadowed.push_back(previous);}
  for(const auto& shadow:e.origin.shadowed)if(origin.shadowed.size()<3)origin.shadowed.push_back(shadow);
  e.value=*value->second;e.origin=origin;
 }return out;
}
`, "__CONFIG_SOURCE__", configSourceName(s)))
}

func configCatalogueRust(b *strings.Builder, s *Definition) {
	if !configCatalogueReady(s) {
		return
	}
	fmt.Fprintf(b, "\npub const CONFIG_CATALOGUE_JSON: &str = %s;\n", strconv.Quote(s.ConfigCatalogue))
	fmt.Fprintf(b, "\npub fn validate_config_value(v: &Value) -> ::std::result::Result<(),Refusal> { if !(%s) { return Err(Refusal{word:\"bad_value\",offset:0}); }\n", configValueCondition(s, "rust", "v.", false))
	b.WriteString("let r=Reader { buf: &[], pos: 0, depth: 0 };\n")
	emitChecks(b, lowerChecks(*s.Struct("Value")), "rust", siteDecode)
	b.WriteString("Value::from_json(&v.to_json()).map(|_|()) }\n")
	b.WriteString(strings.ReplaceAll(`
pub fn config_key_descriptors() -> ::std::vec::Vec<KeyDescriptor> {
 let data=CONFIG_CATALOGUE_JSON.as_bytes();let mut r=Reader { buf: data, pos: 0, depth: 0 };r.skip_ws();let rows=decode_list(&mut r,decode_key_descriptor).expect("validated config catalogue");r.skip_ws();assert_eq!(r.pos,r.buf.len(),"validated catalogue trailing bytes");rows
}
pub fn overlay_config_process(entries:&[Entry],changes:&[Change])->::std::result::Result<::std::vec::Vec<Entry>,Refusal>{
 let descriptors: ::std::collections::BTreeMap<_,_>=config_key_descriptors().into_iter().map(|d|(d.key.clone(),d)).collect();
 let mut pending: ::std::collections::BTreeMap<::std::string::String,Option<Value>>=::std::collections::BTreeMap::new();
 for c in changes {
  let Some(d)=descriptors.get(&c.key) else{return Err(Refusal{word:"bad_value",offset:0})};
  if pending.contains_key(&c.key){return Err(Refusal{word:"bad_value",offset:0})}pending.insert(c.key.clone(),None);
  let Some(v)=&c.set else{continue};validate_config_value(v)?;if(v.kind.as_str()=="unlimited"&&!d.allows_unlimited)||(v.kind.as_str()!="unlimited"&&v.kind!=d.kind){return Err(Refusal{word:"bad_value",offset:0})}
  let n=match v.kind.as_str(){"integer"=>Some(v.integer),"bytes"=>Some(v.bytes),"duration"=>Some(v.duration_ms),_=>None};
  if let(Some(n),Some(range))=(n,&d.range){if n<range.min||n>range.max{return Err(Refusal{word:"bad_value",offset:0})}}
  if v.kind.as_str()=="choice"&&!d.choices.contains(&v.choice){return Err(Refusal{word:"bad_value",offset:0})}
  pending.insert(c.key.clone(),Some(Value::from_json(&v.to_json())?));
 }
 let mut out=::std::vec::Vec::new();for e in entries{out.push(Entry::from_json(&e.to_json())?)}
 for e in &mut out {
  let Some(Some(value))=pending.get(&e.key) else{continue};if e.origin.state=="locked"{continue}
  let d=&descriptors[&e.key];if !["machine","user","process"].contains(&d.lowest_location.as_str()){continue}
  let mut shadows=::std::vec::Vec::new();if let Some(location)=e.origin.location{if location!=Location::Process{shadows.push(__CONFIG_SOURCE__{location,path:e.origin.path.clone()});}}
  for old in &e.origin.shadowed{if shadows.len()<3{shadows.push(__CONFIG_SOURCE__{location:old.location,path:old.path.clone()});}}
  e.value=Some(Value::from_json(&value.to_json())?);e.origin=KeyOrigin{location:Some(Location::Process),path: ::std::string::String::new(),state:"set".to_owned(),reason: ::std::string::String::new(),shadowed:shadows};
 }Ok(out)
}
`, "__CONFIG_SOURCE__", configSourceName(s)))
}

func configSourceName(s *Definition) string {
	name, err := configSourceType(s)
	if err != nil {
		panic(err)
	}
	return name
}
