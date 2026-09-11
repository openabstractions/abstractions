package main

// Calendar arithmetic stays inside the codecs: no clock, locale or platform
// date range may change the wire grammar. Offsets move at most one calendar day.
const goTimestampNormalize = `
func timestampNumber(s string, i, n int) int {
 v := 0
 for k := 0; k < n; k++ { v = v*10 + int(s[i+k]-'0') }
 return v
}
func timestampDays(y, m int) int {
 if m == 2 {
  if y%4 == 0 && (y%100 != 0 || y%400 == 0) { return 29 }; return 28
 }
 if m == 4 || m == 6 || m == 9 || m == 11 { return 30 }; return 31
}
func normalizedTimestamp(s string) string {
 if !lexicalTimestamp(s) { return "" }
 y, m, d := timestampNumber(s,0,4), timestampNumber(s,5,2), timestampNumber(s,8,2)
 h, minute, sec := timestampNumber(s,11,2), timestampNumber(s,14,2), timestampNumber(s,17,2)
 if m < 1 || m > 12 || d < 1 || d > timestampDays(y,m) || h > 23 || minute > 59 || sec > 59 { return "" }
 i, fraction := 19, ""
 if s[i] == '.' { i++; start := i; for s[i] >= '0' && s[i] <= '9' { i++ }; fraction = s[start:i] }
 total := h*60+minute
 if s[i] == '+' || s[i] == '-' {
  oh, om := timestampNumber(s,i+1,2), timestampNumber(s,i+4,2)
  if oh > 23 || om > 59 { return "" }
  offset := oh*60+om
  if s[i] == '+' { total -= offset } else { total += offset }
 }
 if total < 0 { total += 1440; d-- } else if total >= 1440 { total -= 1440; d++ }
 if d == 0 { m--; if m == 0 { y--; m = 12 }; d = timestampDays(y,m) }
 if d > timestampDays(y,m) { d = 1; m++; if m == 13 { y++; m = 1 } }
 if y < 0 || y > 9999 { return "" }
 out := []byte("0000-00-00T00:00:00.000000Z")
 put := func(at, width, value int) { for k:=width-1; k>=0; k-- { out[at+k]=byte(value%10)+'0'; value/=10 } }
 put(0,4,y); put(5,2,m); put(8,2,d); put(11,2,total/60); put(14,2,total%60); put(17,2,sec)
 for k:=0; k<6 && k<len(fraction); k++ { out[20+k]=fraction[k] }
 return string(out)
}
func WideTimestamp(s string) bool { return normalizedTimestamp(s) != "" }
func writeTimestamp(s string) string {
 result := normalizedTimestamp(s)
 if result == "" { panic(&Refusal{Word:"bad_timestamp", Offset:0}) }
 return result
}
`

const pyTimestampNormalize = `
def _timestamp_days(y, m):
    if m == 2:
        return 29 if y % 4 == 0 and (y % 100 != 0 or y % 400 == 0) else 28
    return 30 if m in (4, 6, 9, 11) else 31


def _normalized_timestamp(s):
    if not _lexical_timestamp(s):
        return ""
    y, m, d = int(s[:4]), int(s[5:7]), int(s[8:10])
    h, minute, sec = int(s[11:13]), int(s[14:16]), int(s[17:19])
    if not 1 <= m <= 12 or not 1 <= d <= _timestamp_days(y, m) or h > 23 or minute > 59 or sec > 59:
        return ""
    i, fraction = 19, ""
    if s[i] == ".":
        i += 1
        start = i
        while "0" <= s[i] <= "9":
            i += 1
        fraction = s[start:i]
    total = h * 60 + minute
    if s[i] in "+-":
        oh, om = int(s[i+1:i+3]), int(s[i+4:i+6])
        if oh > 23 or om > 59:
            return ""
        offset = oh * 60 + om
        total += -offset if s[i] == "+" else offset
    if total < 0:
        total += 1440
        d -= 1
    elif total >= 1440:
        total -= 1440
        d += 1
    if d == 0:
        m -= 1
        if m == 0:
            y, m = y - 1, 12
        d = _timestamp_days(y, m)
    if d > _timestamp_days(y, m):
        d, m = 1, m + 1
        if m == 13:
            y, m = y + 1, 1
    if not 0 <= y <= 9999:
        return ""
    return "%04d-%02d-%02dT%02d:%02d:%02d.%sZ" % (y, m, d, total // 60, total % 60, sec, (fraction + "000000")[:6])


def wide_timestamp(s):
    return bool(_normalized_timestamp(s))


def _write_timestamp(s):
    result = _normalized_timestamp(s)
    if not result:
        raise Refusal("bad_timestamp", 0)
    return result
`

const jsTimestampNormalize = `
function timestampDays(y, m) {
  if (m === 2) return y % 4 === 0 && (y % 100 !== 0 || y % 400 === 0) ? 29 : 28;
  return [4, 6, 9, 11].includes(m) ? 30 : 31;
}
function normalizedTimestamp(s) {
  if (!lexicalTimestamp(s)) return "";
  let y=Number(s.slice(0,4)), m=Number(s.slice(5,7)), d=Number(s.slice(8,10));
  const h=Number(s.slice(11,13)), minute=Number(s.slice(14,16)), sec=Number(s.slice(17,19));
  if (m<1 || m>12 || d<1 || d>timestampDays(y,m) || h>23 || minute>59 || sec>59) return "";
  let i=19, fraction="";
  if (s[i]===".") { const start=++i; while(s[i]>="0" && s[i]<="9") i++; fraction=s.slice(start,i); }
  let total=h*60+minute;
  if(s[i]==="+" || s[i]==="-") {
    const oh=Number(s.slice(i+1,i+3)), om=Number(s.slice(i+4,i+6));
    if(oh>23 || om>59) return "";
    total += (s[i]==="+" ? -1 : 1)*(oh*60+om);
  }
  if(total<0) {total+=1440; d--;} else if(total>=1440) {total-=1440; d++;}
  if(d===0) {m--; if(m===0) {y--; m=12;} d=timestampDays(y,m);}
  if(d>timestampDays(y,m)) {d=1; m++; if(m===13) {y++; m=1;}}
  if(y<0 || y>9999) return "";
  const pad=(n,w)=>String(n).padStart(w,"0");
  return pad(y,4)+"-"+pad(m,2)+"-"+pad(d,2)+"T"+pad(Math.floor(total/60),2)+":"+pad(total%60,2)+":"+pad(sec,2)+"."+(fraction+"000000").slice(0,6)+"Z";
}
export function wideTimestamp(s) { return normalizedTimestamp(s)!==""; }
function writeTimestamp(s) {
  const result=normalizedTimestamp(s);
  if(result==="") throw new Refusal("bad_timestamp",0);
  return result;
}
`

const cppTimestampNormalize = `
inline int timestamp_number(std::string_view s, std::size_t at, std::size_t n) {
    int v=0; for(std::size_t k=0;k<n;++k) v=v*10+(s[at+k]-'0'); return v;
}
inline int timestamp_days(int y, int m) {
    if(m==2) return y%4==0 && (y%100!=0 || y%400==0) ? 29 : 28;
    return m==4 || m==6 || m==9 || m==11 ? 30 : 31;
}
inline std::string normalized_timestamp(std::string_view s) {
    if(!lexical_timestamp(s)) return {};
    int y=timestamp_number(s,0,4), m=timestamp_number(s,5,2), d=timestamp_number(s,8,2);
    int h=timestamp_number(s,11,2), minute=timestamp_number(s,14,2), sec=timestamp_number(s,17,2);
    if(m<1 || m>12 || d<1 || d>timestamp_days(y,m) || h>23 || minute>59 || sec>59) return {};
    std::size_t i=19; std::string_view fraction;
    if(s[i]=='.') {const auto start=++i; while(s[i]>='0' && s[i]<='9') ++i; fraction=s.substr(start,i-start);}
    int total=h*60+minute;
    if(s[i]=='+' || s[i]=='-') {
        int oh=timestamp_number(s,i+1,2), om=timestamp_number(s,i+4,2);
        if(oh>23 || om>59) return {};
        total += (s[i]=='+' ? -1 : 1)*(oh*60+om);
    }
    if(total<0) {total+=1440; --d;} else if(total>=1440) {total-=1440; ++d;}
    if(d==0) {--m; if(m==0) {--y; m=12;} d=timestamp_days(y,m);}
    if(d>timestamp_days(y,m)) {d=1; ++m; if(m==13) {++y; m=1;}}
    if(y<0 || y>9999) return {};
    std::string out="0000-00-00T00:00:00.000000Z";
    auto put=[&](int at,int width,int value) {for(int k=width-1;k>=0;--k) {out[at+k]=char('0'+value%10); value/=10;}};
    put(0,4,y); put(5,2,m); put(8,2,d); put(11,2,total/60); put(14,2,total%60); put(17,2,sec);
    for(std::size_t k=0;k<6 && k<fraction.size();++k) out[20+k]=fraction[k];
    return out;
}
inline bool wide_timestamp(std::string_view s) {return !normalized_timestamp(s).empty();}
inline std::string write_timestamp(std::string_view s) {
    auto result=normalized_timestamp(s);
    if(result.empty()) throw Refusal("bad_timestamp",0);
    return result;
}
`

const rsTimestampNormalize = `
fn timestamp_number(s: &[u8], at: usize, n: usize) -> i32 {
    s[at..at+n].iter().fold(0, |v,c| v*10+i32::from(c-b'0'))
}
fn timestamp_days(y: i32, m: i32) -> i32 {
    if m==2 {return if y%4==0 && (y%100!=0 || y%400==0) {29} else {28};}
    if m==4 || m==6 || m==9 || m==11 {30} else {31}
}
fn normalized_timestamp(text: &str) -> String {
    if !lexical_timestamp(text) {return String::new();}
    let s=text.as_bytes();
    let (mut y,mut m,mut d)=(timestamp_number(s,0,4),timestamp_number(s,5,2),timestamp_number(s,8,2));
    let (h,minute,sec)=(timestamp_number(s,11,2),timestamp_number(s,14,2),timestamp_number(s,17,2));
    if m<1 || m>12 || d<1 || d>timestamp_days(y,m) || h>23 || minute>59 || sec>59 {return String::new();}
    let mut i=19; let mut fraction=&s[0..0];
    if s[i]==b'.' {i+=1; let start=i; while s[i].is_ascii_digit() {i+=1;} fraction=&s[start..i];}
    let mut total=h*60+minute;
    if s[i]==b'+' || s[i]==b'-' {
        let (oh,om)=(timestamp_number(s,i+1,2),timestamp_number(s,i+4,2));
        if oh>23 || om>59 {return String::new();}
        total += (if s[i]==b'+' {-1} else {1})*(oh*60+om);
    }
    if total<0 {total+=1440; d-=1;} else if total>=1440 {total-=1440; d+=1;}
    if d==0 {m-=1; if m==0 {y-=1; m=12;} d=timestamp_days(y,m);}
    if d>timestamp_days(y,m) {d=1; m+=1; if m==13 {y+=1; m=1;}}
    if !(0..=9999).contains(&y) {return String::new();}
    let mut out=*b"0000-00-00T00:00:00.000000Z";
    let mut put=|at: usize,width: usize,mut value: i32| {for k in (0..width).rev() {out[at+k]=b'0'+(value%10) as u8; value/=10;}};
    put(0,4,y); put(5,2,m); put(8,2,d); put(11,2,total/60); put(14,2,total%60); put(17,2,sec);
    for k in 0..fraction.len().min(6) {out[20+k]=fraction[k];}
    String::from_utf8(out.to_vec()).unwrap()
}
pub fn wide_timestamp(text: &str) -> bool {!normalized_timestamp(text).is_empty()}
fn write_timestamp(text: &str) -> String {
    let result=normalized_timestamp(text);
    assert!(!result.is_empty(), "bad_timestamp");
    result
}
`
