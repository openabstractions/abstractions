package main

import "fmt"

func hasBinary(s *Definition) bool {
	for _, st := range s.Structs {
		for _, f := range st.Fields {
			if f.Type == "binary" {
				return true
			}
		}
	}
	for _, svc := range s.Services {
		for _, m := range svc.Methods {
			if m.Result.Type == "binary" {
				return true
			}
			for _, f := range m.Args {
				if f.Type == "binary" {
					return true
				}
			}
		}
	}
	return false
}
func validateBinaryBackend(s *Definition, lang string) error {
	if hasBinary(s) && lang != "go" && lang != "cpp" && lang != "python" && lang != "docs" {
		return fmt.Errorf("%s binary generation is not implemented", lang)
	}
	return nil
}

const goBinary = `
func encodeBinary(value []byte) string {
 const alphabet="ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
 out:=make([]byte,0,((len(value)+2)/3)*4)
 for i:=0;i<len(value);i+=3 {
  n:=len(value)-i; v:=uint32(value[i])<<16
  if n>1 {v|=uint32(value[i+1])<<8};if n>2 {v|=uint32(value[i+2])}
  out=append(out,alphabet[v>>18],alphabet[(v>>12)&63])
  if n>1 {out=append(out,alphabet[(v>>6)&63])}else{out=append(out,'=')}
  if n>2 {out=append(out,alphabet[v&63])}else{out=append(out,'=')}
 }
 return string(out)
}
func(r *reader)binary()([]byte,error){
 text,err:=r.str();if err!=nil{return nil,err}
 const alphabet="ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
 if len(text)%4!=0{return nil,r.refuse("bad_binary")}
 out:=make([]byte,0,len(text)/4*3)
 for i:=0;i<len(text);i+=4{
  var v uint32; pad:=0
  for j:=0;j<4;j++{c:=text[i+j];digit:=-1
   if c=='=' {if j<2||i+4!=len(text){return nil,r.refuse("bad_binary")};pad++;digit=0
   }else{if pad!=0{return nil,r.refuse("bad_binary")};for k:=0;k<len(alphabet);k++{if alphabet[k]==c{digit=k;break}}}
   if digit<0{return nil,r.refuse("bad_binary")};v=(v<<6)|uint32(digit)
  }
  out=append(out,byte(v>>16));if pad<2{out=append(out,byte(v>>8))};if pad==0{out=append(out,byte(v))}
 }
 if encodeBinary(out)!=text{return nil,r.refuse("bad_binary")}
 return out,nil
}
`
const cppBinary = `
inline std::string encode_binary(const std::vector<std::uint8_t>& value){
 const char* alphabet="ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
 std::string out;
 for(std::size_t i=0;i<value.size();i+=3){auto n=value.size()-i;std::uint32_t v=std::uint32_t(value[i])<<16;if(n>1)v|=std::uint32_t(value[i+1])<<8;if(n>2)v|=value[i+2];out+=alphabet[v>>18];out+=alphabet[(v>>12)&63];out+=n>1?alphabet[(v>>6)&63]:'=';out+=n>2?alphabet[v&63]:'=';}
 return out;
}
inline std::vector<std::uint8_t> decode_binary(const std::string& text){
 auto refuse=[](const char* word){throw Refusal(word,0);};
 const std::string alphabet="ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
 if(text.size()%4)refuse("bad_binary");
 std::vector<std::uint8_t> out;
 for(std::size_t i=0;i<text.size();i+=4){std::uint32_t v=0;int pad=0;
  for(int j=0;j<4;j++){auto c=text[i+j];std::size_t digit=0;
   if(c=='='){if(j<2||i+4!=text.size())refuse("bad_binary");pad++;}
   else{if(pad)refuse("bad_binary");digit=alphabet.find(c);if(digit==std::string::npos)refuse("bad_binary");}
   v=(v<<6)|static_cast<std::uint32_t>(digit);
  }
  out.push_back(static_cast<std::uint8_t>(v>>16));if(pad<2)out.push_back(static_cast<std::uint8_t>(v>>8));if(!pad)out.push_back(static_cast<std::uint8_t>(v));
 }
 if(encode_binary(out)!=text)refuse("bad_binary");return out;
}
`
const pyBinary = `
def _encode_binary(value):
    if type(value) is not bytes:
        raise Refusal("wrong_type", 0)
    alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
    out = []
    for i in range(0, len(value), 3):
        chunk = value[i:i+3]
        v = int.from_bytes(chunk, "big") << (8 * (3-len(chunk)))
        out.extend((alphabet[v >> 18], alphabet[(v >> 12) & 63], alphabet[(v >> 6) & 63] if len(chunk)>1 else "=", alphabet[v & 63] if len(chunk)>2 else "="))
    return "".join(out)

def _decode_binary(text):
    alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
    def bad():
        raise _Reader(b"").refuse("bad_binary")
    if len(text) % 4: bad()
    out = bytearray()
    for i in range(0,len(text),4):
        v, pad = 0, 0
        for j,c in enumerate(text[i:i+4]):
            if c == "=":
                if j<2 or i+4 != len(text): bad()
                pad += 1
                digit = 0
            else:
                if pad: bad()
                digit = alphabet.find(c)
                if digit < 0: bad()
            v = (v << 6) | digit
        out.append(v >> 16)
        if pad < 2: out.append((v >> 8) & 255)
        if pad == 0: out.append(v & 255)
    result = bytes(out)
    if _encode_binary(result) != text: bad()
    return result
`
