package main

import (
	"fmt"
	"strconv"
	"strings"
)

const cppEscMinimal = `
inline void esc(std::string& out, const std::string& s) {
    out += '"';
    for (unsigned char c : s) esc_byte(out, c);
    out += '"';
}
`

const cppEscASCII = `
inline void unit(std::string& out, std::uint32_t u) {
    static const char* kHex = "0123456789abcdef";
    out += "\\u";
    out += kHex[(u >> 12) & 0xF];
    out += kHex[(u >> 8) & 0xF];
    out += kHex[(u >> 4) & 0xF];
    out += kHex[u & 0xF];
}

inline void esc(std::string& out, const std::string& s) {
    out += '"';
    for (std::size_t i = 0; i < s.size();) {
        const unsigned char c = static_cast<unsigned char>(s[i]);
        if (c < 0x80) {
            esc_byte(out, c);
            ++i;
            continue;
        }
        std::uint32_t cp = 0;
        std::size_t n = 0;
        if (c >= 0xF0) {
            cp = (c & 0x07u) << 18 | (s[i + 1] & 0x3Fu) << 12 | (s[i + 2] & 0x3Fu) << 6 | (s[i + 3] & 0x3Fu);
            n = 4;
        } else if (c >= 0xE0) {
            cp = (c & 0x0Fu) << 12 | (s[i + 1] & 0x3Fu) << 6 | (s[i + 2] & 0x3Fu);
            n = 3;
        } else {
            cp = (c & 0x1Fu) << 6 | (s[i + 1] & 0x3Fu);
            n = 2;
        }
        if (cp > 0xFFFF) {
            cp -= 0x10000;
            unit(out, 0xD800 + (cp >> 10));
            unit(out, 0xDC00 + (cp & 0x3FF));
        } else {
            unit(out, cp);
        }
        i += n;
    }
    out += '"';
}
`

const cppCommon = `#pragma once

#include <cstddef>
#include <cstdint>
#include <map>
#include <optional>
#include <stdexcept>
#include <string>
#include <string_view>
#include <vector>

namespace rec {

using Raw = std::string;

inline void esc(std::string& out, const std::string& s);

inline void esc_byte(std::string& out, unsigned char c) {
    static const char* kHex = "0123456789abcdef";
    switch (c) {
        case '"': out += "\\\""; return;
        case '\\': out += "\\\\"; return;
        case 0x08: out += "\\b"; return;
        case 0x0c: out += "\\f"; return;
        case '\n': out += "\\n"; return;
        case '\r': out += "\\r"; return;
        case '\t': out += "\\t"; return;
        default: break;
    }
    if (c < 0x20) {
        out += "\\u00";
        out += kHex[c >> 4];
        out += kHex[c & 0x0F];
    } else {
        out += static_cast<char>(c);
    }
}

inline void num(std::string& out, std::int64_t n) { out += std::to_string(n); }

inline void pad(std::string& out, int depth) {
    out.append(static_cast<std::size_t>(depth) * @INDENT@, ' ');
}

inline void strs(std::string& out, const std::vector<std::string>& v, int depth) {
    if (v.empty()) { out += "[]"; return; }
    out += "[\n";
    for (std::size_t i = 0; i < v.size(); ++i) {
        pad(out, depth + 1);
        esc(out, v[i]);
        if (i + 1 < v.size()) out += ',';
        out += '\n';
    }
    pad(out, depth);
    out += ']';
}

inline bool ws(unsigned char c) { return c == ' ' || c == '\t' || c == '\n' || c == '\r'; }

inline void raw(std::string& out, const std::string& s, int depth) {
    const std::size_t n = s.size();
    for (std::size_t i = 0; i < n;) {
        const unsigned char c = static_cast<unsigned char>(s[i]);
        if (ws(c)) {
            ++i;
        } else if (c == '"') {
            std::size_t j = i + 1;
            while (j < n) {
                if (s[j] == '\\') { j += 2; continue; }
                if (s[j] == '"') { ++j; break; }
                ++j;
            }
            out.append(s, i, j - i);
            i = j;
        } else if (c == '{' || c == '[') {
            out += static_cast<char>(c);
            ++i;
            std::size_t j = i;
            while (j < n && ws(static_cast<unsigned char>(s[j]))) ++j;
            if (j < n && (s[j] == '}' || s[j] == ']')) {
                out += s[j];
                i = j + 1;
            } else {
                ++depth;
                out += '\n';
                pad(out, depth);
            }
        } else if (c == '}' || c == ']') {
            --depth;
            out += '\n';
            pad(out, depth);
            out += static_cast<char>(c);
            ++i;
        } else if (c == ',') {
            out += ",\n";
            pad(out, depth);
            ++i;
        } else if (c == ':') {
            out += ": ";
            ++i;
        } else {
            out += static_cast<char>(c);
            ++i;
        }
    }
}

// std::char_traits<char>::compare is specified to order by unsigned char, so a
// std::map<std::string, ...> already walks its keys in UTF-8 byte order — which
// is what the definition declares. Nothing sorts here because nothing needs to.
inline void rawmap(std::string& out, const std::map<std::string, Raw>& m, int depth) {
    if (m.empty()) { out += "{}"; return; }
    out += "{\n";
    std::size_t i = 0;
    for (const auto& kv : m) {
        pad(out, depth + 1);
        esc(out, kv.first);
        out += ": ";
        raw(out, kv.second, depth + 1);
        if (++i < m.size()) out += ',';
        out += '\n';
    }
    pad(out, depth);
    out += '}';
}
`

const cppStrMap = `
inline void strmap(std::string& out, const std::map<std::string, std::string>& m, int depth) {
    if (m.empty()) { out += "{}"; return; }
    out += "{\n";
    std::size_t i = 0;
    for (const auto& kv : m) {
        pad(out, depth + 1);
        esc(out, kv.first);
        out += ": ";
        esc(out, kv.second);
        if (++i < m.size()) out += ',';
        out += '\n';
    }
    pad(out, depth);
    out += '}';
}
`

const cppEncList = `
template <typename T>
inline void enc_list(std::string& out, const std::vector<T>& v, int depth,
                     void (*enc)(std::string&, const T&, int)) {
    if (v.empty()) { out += "[]"; return; }
    out += "[\n";
    for (std::size_t i = 0; i < v.size(); ++i) {
        pad(out, depth + 1);
        enc(out, v[i], depth + 1);
        if (i + 1 < v.size()) out += ',';
        out += '\n';
    }
    pad(out, depth);
    out += ']';
}
`

const cppDecodeCommon = `
inline constexpr int kDepthLimit = @DEPTH@;
inline constexpr std::size_t kI64Digits = 19;

class Refusal : public std::runtime_error {
public:
    Refusal(const char* word, std::size_t offset)
        : std::runtime_error(std::string("refused: ") + word + " at byte " + std::to_string(offset)),
          word(word),
          offset(offset) {}
    const char* word;
    std::size_t offset;
};

inline void append_rune(std::string& out, std::uint32_t cp) {
    if (cp < 0x80) {
        out += static_cast<char>(cp);
    } else if (cp < 0x800) {
        out += static_cast<char>(0xC0 | (cp >> 6));
        out += static_cast<char>(0x80 | (cp & 0x3F));
    } else if (cp < 0x10000) {
        out += static_cast<char>(0xE0 | (cp >> 12));
        out += static_cast<char>(0x80 | ((cp >> 6) & 0x3F));
        out += static_cast<char>(0x80 | (cp & 0x3F));
    } else {
        out += static_cast<char>(0xF0 | (cp >> 18));
        out += static_cast<char>(0x80 | ((cp >> 12) & 0x3F));
        out += static_cast<char>(0x80 | ((cp >> 6) & 0x3F));
        out += static_cast<char>(0x80 | (cp & 0x3F));
    }
}

inline bool valid_utf8(const std::string& b) {
    for (std::size_t i = 0; i < b.size();) {
        const unsigned char c = static_cast<unsigned char>(b[i]);
        if (c < 0x80) { ++i; continue; }
        std::size_t n = 0;
        std::uint32_t cp = 0;
        if ((c >> 5) == 0x6) { n = 2; cp = c & 0x1Fu; }
        else if ((c >> 4) == 0xE) { n = 3; cp = c & 0x0Fu; }
        else if ((c >> 3) == 0x1E) { n = 4; cp = c & 0x07u; }
        else return false;
        if (i + n > b.size()) return false;
        for (std::size_t k = 1; k < n; ++k) {
            const unsigned char t = static_cast<unsigned char>(b[i + k]);
            if ((t >> 6) != 0x2) return false;
            cp = cp << 6 | (t & 0x3Fu);
        }
        static const std::uint32_t kLowest[5] = {0, 0, 0x80, 0x800, 0x10000};
        if (cp < kLowest[n] || cp > 0x10FFFF || (cp >= 0xD800 && cp <= 0xDFFF)) return false;
        i += n;
    }
    return true;
}

struct Reader {
    std::string_view buf;
    std::size_t pos = 0;
    int depth = 0;

    [[noreturn]] void refuse(const char* word) const { throw Refusal(word, pos); }

    unsigned char at() const {
        return pos < buf.size() ? static_cast<unsigned char>(buf[pos]) : 0;
    }

    bool digit() const { return at() >= '0' && at() <= '9'; }

    void skip_ws() {
        while (pos < buf.size() && ws(static_cast<unsigned char>(buf[pos]))) ++pos;
    }

    void enter() {
        if (++depth > kDepthLimit) refuse("depth_exceeded");
    }

    std::string str() {
        if (at() != '"') refuse("wrong_type");
        ++pos;
        std::string out;
        for (;;) {
            if (pos >= buf.size()) refuse("malformed");
            const unsigned char c = static_cast<unsigned char>(buf[pos]);
            if (c == '"') {
                ++pos;
                if (!valid_utf8(out)) refuse("bad_string");
                return out;
            }
            if (c < 0x20) refuse("bad_string");
            if (c == '\\') {
                escape(out);
            } else {
                out += static_cast<char>(c);
                ++pos;
            }
        }
    }

    void escape(std::string& out) {
        ++pos;
        if (pos >= buf.size()) refuse("malformed");
        const unsigned char c = static_cast<unsigned char>(buf[pos]);
        ++pos;
        switch (c) {
            case '"': case '\\': case '/': out += static_cast<char>(c); return;
            case 'b': out += static_cast<char>(0x08); return;
            case 'f': out += static_cast<char>(0x0c); return;
            case 'n': out += '\n'; return;
            case 'r': out += '\r'; return;
            case 't': out += '\t'; return;
            case 'u': break;
            default: refuse("bad_string");
        }
        std::uint32_t u = hex4();
        if (u >= 0xDC00 && u <= 0xDFFF) refuse("bad_string");
        if (u >= 0xD800 && u <= 0xDBFF) {
            if (pos + 1 >= buf.size() || buf[pos] != '\\' || buf[pos + 1] != 'u') refuse("bad_string");
            pos += 2;
            const std::uint32_t low = hex4();
            if (low < 0xDC00 || low > 0xDFFF) refuse("bad_string");
            u = 0x10000 + ((u - 0xD800) << 10) + (low - 0xDC00);
        }
        append_rune(out, u);
    }

    std::uint32_t hex4() {
        if (pos + 4 > buf.size()) refuse("bad_string");
        std::uint32_t u = 0;
        for (std::size_t i = 0; i < 4; ++i) {
            const unsigned char c = static_cast<unsigned char>(buf[pos + i]);
            if (c >= '0' && c <= '9') u = u << 4 | static_cast<std::uint32_t>(c - '0');
            else if (c >= 'a' && c <= 'f') u = u << 4 | static_cast<std::uint32_t>(c - 'a' + 10);
            else if (c >= 'A' && c <= 'F') u = u << 4 | static_cast<std::uint32_t>(c - 'A' + 10);
            else refuse("bad_string");
        }
        pos += 4;
        return u;
    }

    std::int64_t integer(std::int64_t low, std::int64_t high) {
        const unsigned char c = at();
        if (c != '-' && !digit()) refuse("wrong_type");
        const bool negative = c == '-';
        if (negative) ++pos;
        if (!digit()) refuse("malformed");
        std::uint64_t u = 0;
        if (at() == '0') {
            ++pos;
        } else {
            while (digit()) {
                if (u > 922337203685477580ULL) refuse("number_spelling");
                u = u * 10 + static_cast<std::uint64_t>(at() - '0');
                ++pos;
            }
        }
        const unsigned char after = at();
        if (after == '.' || after == 'e' || after == 'E' || digit()) refuse("number_spelling");
        if (negative && u == 0) refuse("number_spelling");
        std::int64_t n = 0;
        if (negative) {
            if (u > (1ULL << 63)) refuse("number_spelling");
            n = u == (1ULL << 63) ? INT64_MIN : -static_cast<std::int64_t>(u);
        } else {
            if (u > static_cast<std::uint64_t>(INT64_MAX)) refuse("number_spelling");
            n = static_cast<std::int64_t>(u);
        }
        if (n < low || n > high) refuse("number_spelling");
        return n;
    }

    bool boolean() {
        if (at() == 't') { literal("true"); return true; }
        if (at() == 'f') { literal("false"); return false; }
        refuse("wrong_type");
    }

    void literal(std::string_view word) {
        if (buf.substr(pos, word.size()) != word) refuse("malformed");
        pos += word.size();
    }

    std::vector<std::string> str_list() {
        if (at() != '[') refuse("wrong_type");
        enter();
        ++pos;
        std::vector<std::string> out;
        skip_ws();
        if (at() != ']') {
            for (;;) {
                skip_ws();
                out.push_back(str());
                skip_ws();
                if (at() != ',') break;
                ++pos;
            }
        }
        if (at() != ']') refuse("malformed");
        ++pos;
        --depth;
        return out;
    }

    std::map<std::string, Raw> raw_map() {
        if (at() != '{') refuse("wrong_type");
        enter();
        ++pos;
        std::map<std::string, Raw> out;
        skip_ws();
        if (at() != '}') {
            for (;;) {
                skip_ws();
                if (at() != '"') refuse("malformed");
                const std::string k = str();
@DUPKEY@                skip_ws();
                if (at() != ':') refuse("malformed");
                ++pos;
                skip_ws();
                out[k] = raw_value();
                skip_ws();
                if (at() != ',') break;
                ++pos;
            }
        }
        if (at() != '}') refuse("malformed");
        ++pos;
        --depth;
        return out;
    }

    Raw raw_value() {
        const std::size_t start = pos;
        skip_value();
        return std::string(buf.substr(start, pos - start));
    }

    void skip_value() {
        const unsigned char c = at();
        if (c == '"') skip_string();
        else if (c == '{' || c == '[') skip_container();
        else if (c == 't') literal("true");
        else if (c == 'f') literal("false");
        else if (c == 'n') literal("null");
        else if (c == '-' || digit()) skip_number();
        else refuse("malformed");
    }

    // An opaque value is validated and carried, never interpreted. Its syntax is
    // the record's own — the same string reader, so an escape, a control byte or
    // a lone surrogate is judged identically at any depth — and what it is free
    // to spell, it keeps: the bytes between these two offsets are what a writer
    // re-emits. A number is walked, never converted, so a value no host type
    // holds survives the reader that carried it.
    void skip_string() { str(); }

    void some_digits() {
        int n = 0;
        while (digit()) { ++pos; ++n; }
        if (n == 0) refuse("number_spelling");
    }

    void skip_number() {
        if (at() == '-') ++pos;
        const unsigned char c = at();
        if (c == '0') ++pos;
        else if (c >= '1' && c <= '9') { while (digit()) ++pos; }
        else refuse("number_spelling");
        if (at() == '.') { ++pos; some_digits(); }
        if (at() == 'e' || at() == 'E') {
            ++pos;
            if (at() == '+' || at() == '-') ++pos;
            some_digits();
        }
    }

    void skip_container() {
        const unsigned char opener = at();
        const char shut = opener == '{' ? '}' : ']';
        enter();
        ++pos;
        skip_ws();
@SKIPSEEN@        if (at() != static_cast<unsigned char>(shut)) {
            for (;;) {
                skip_ws();
                if (opener == '{') {
                    if (at() != '"') refuse("malformed");
                    @SKIPKEY@str();
@SKIPDUP@                    skip_ws();
                    if (at() != ':') refuse("malformed");
                    ++pos;
                    skip_ws();
                }
                skip_value();
                skip_ws();
                if (at() != ',') break;
                ++pos;
            }
        }
        if (at() != static_cast<unsigned char>(shut)) refuse("malformed");
        ++pos;
        --depth;
    }
};
`

const cppStrMapDecode = `
inline std::map<std::string, std::string> str_map(Reader& r) {
    if (r.at() != '{') r.refuse("wrong_type");
    r.enter();
    ++r.pos;
    std::map<std::string, std::string> out;
    r.skip_ws();
    if (r.at() != '}') {
        for (;;) {
            r.skip_ws();
            if (r.at() != '"') r.refuse("malformed");
            const std::string k = r.str();
@STRDUP@            r.skip_ws();
            if (r.at() != ':') r.refuse("malformed");
            ++r.pos;
            r.skip_ws();
            out[k] = r.str();
            r.skip_ws();
            if (r.at() != ',') break;
            ++r.pos;
        }
    }
    if (r.at() != '}') r.refuse("malformed");
    ++r.pos;
    --r.depth;
    return out;
}
`

const cppDecodeList = `
template <typename T>
inline std::vector<T> decode_list(Reader& r, T (*elem)(Reader&)) {
    if (r.at() != '[') r.refuse("wrong_type");
    r.enter();
    ++r.pos;
    std::vector<T> out;
    r.skip_ws();
    if (r.at() != ']') {
        for (;;) {
            r.skip_ws();
            out.push_back(elem(r));
            r.skip_ws();
            if (r.at() != ',') break;
            ++r.pos;
        }
    }
    if (r.at() != ']') r.refuse("malformed");
    ++r.pos;
    --r.depth;
    return out;
}
`

const cppDupKeyRefuse = `                if (out.count(k) != 0) refuse("duplicate_key");
`

const cppStrDupKey = `            if (out.count(k) != 0) r.refuse("duplicate_key");
`

const cppSkipDupKey = `                    if (!seen.emplace(k, true).second) refuse("duplicate_key");
`

const cppTimestamp = `
inline bool digits(std::string_view s, std::size_t i, std::size_t n) {
    if (s.size() < i + n) return false;
    for (std::size_t k = 0; k < n; ++k) {
        if (s[i + k] < '0' || s[i + k] > '9') return false;
    }
    return true;
}

inline bool date_part(std::string_view s) {
    return s.size() >= 19 && digits(s, 0, 4) && s[4] == '-' && digits(s, 5, 2) && s[7] == '-' &&
           digits(s, 8, 2) && digits(s, 11, 2) && s[13] == ':' && digits(s, 14, 2) && s[16] == ':' &&
           digits(s, 17, 2);
}

// [DEF-G1] rfc3339-wide: what a reader accepts. Any fraction of one to nine
// digits or none, either case of the separators, and a numeric offset.
inline bool wide_timestamp(std::string_view s) {
    if (!date_part(s) || (s[10] != 'T' && s[10] != 't')) return false;
    std::size_t i = 19;
    if (i < s.size() && s[i] == '.') {
        ++i;
        const std::size_t start = i;
        while (i < s.size() && s[i] >= '0' && s[i] <= '9') ++i;
        if (i - start < 1 || i - start > 9) return false;
    }
    if (i >= s.size()) return false;
    if (s[i] == 'Z' || s[i] == 'z') return i + 1 == s.size();
    if (s[i] != '+' && s[i] != '-') return false;
    return s.size() == i + 6 && digits(s, i + 1, 2) && s[i + 3] == ':' && digits(s, i + 4, 2);
}

// [DEF-G2] rfc3339-micros: what a writer emits. Exactly six fractional digits,
// upper-case separators, UTC.
inline bool micros_timestamp(std::string_view s) {
    return s.size() == 27 && date_part(s) && s[10] == 'T' && s[19] == '.' && digits(s, 20, 6) &&
           s[26] == 'Z';
}

inline std::string read_timestamp(Reader& r) {
    const std::size_t at = r.pos;
    const std::string s = r.str();
    if (!wide_timestamp(s)) {
        r.pos = at;
        r.refuse("bad_timestamp");
    }
    return s;
}
`

func cppDecoder(b *strings.Builder, s *Definition) {
	b.WriteString("\n")
	for _, st := range s.Structs {
		fmt.Fprintf(b, "inline %s decode_%s(Reader& r);\n", st.Name, lower(st.Name))
	}
	for _, st := range s.Structs {
		cppStructDecoder(b, s, st)
	}
	cppDerive(b, s)
	if s.Document == "" {
		return
	}
	fmt.Fprintf(b, "\ninline %s decode(std::string_view data) {\n    Reader r{data};\n    r.skip_ws();\n", s.Document)
	fmt.Fprintf(b, "    %s v = decode_%s(r);\n    r.skip_ws();\n", s.Document, lower(s.Document))
	b.WriteString("    if (r.pos < r.buf.size()) r.refuse(\"trailing_bytes\");\n")
	if s.Vocab != nil {
		b.WriteString("    derive(r, v);\n")
	}
	b.WriteString("    return v;\n}\n")
}

func cppStructDecoder(b *strings.Builder, s *Definition, st Struct) {
	fmt.Fprintf(b, "\ninline %s decode_%s(Reader& r) {\n", st.Name, lower(st.Name))
	b.WriteString("    if (r.at() != '{') r.refuse(\"wrong_type\");\n    r.enter();\n    ++r.pos;\n")
	fmt.Fprintf(b, "    %s v;\n    std::uint32_t seen = 0;\n    r.skip_ws();\n", st.Name)
	b.WriteString("    if (r.at() != '}') {\n        for (;;) {\n            r.skip_ws();\n")
	b.WriteString("            if (r.at() != '\"') r.refuse(\"malformed\");\n            const std::string key = r.str();\n")
	b.WriteString("            r.skip_ws();\n            if (r.at() != ':') r.refuse(\"malformed\");\n            ++r.pos;\n            r.skip_ws();\n")
	for i, f := range st.Fields {
		kw := "} else if"
		if i == 0 {
			kw = "            if"
		}
		fmt.Fprintf(b, "%s (key == %q) {\n", kw, f.Name)
		fmt.Fprintf(b, "                if (seen & %du) r.refuse(\"duplicate_field\");\n", 1<<i)
		fmt.Fprintf(b, "                seen |= %du;\n", 1<<i)
		fmt.Fprintf(b, "                v.%s = %s;\n            ", f.Ident("cpp"), cppRead(s, f))
	}
	b.WriteString("} else {\n")
	if st.RefuseUnknown() {
		b.WriteString("                r.refuse(\"unknown_field\");\n")
	} else {
		b.WriteString("                r.skip_value();\n")
	}
	b.WriteString("            }\n            r.skip_ws();\n            if (r.at() != ',') break;\n            ++r.pos;\n        }\n    }\n")
	b.WriteString("    if (r.at() != '}') r.refuse(\"malformed\");\n    ++r.pos;\n    --r.depth;\n")
	if req := requiredMask(st); req != 0 {
		fmt.Fprintf(b, "    if ((seen & %du) != %du) r.refuse(\"missing_field\");\n", req, req)
	}
	b.WriteString("    return v;\n}\n")
}

func cppRead(s *Definition, f Field) string {
	switch f.Type {
	case "string":
		if f.Grammar.Named() {
			return "read_timestamp(r)"
		}
		return "r.str()"
	case "i32":
		return "static_cast<std::int32_t>(r.integer(INT32_MIN, INT32_MAX))"
	case "i64":
		return "r.integer(INT64_MIN, INT64_MAX)"
	case "bool":
		return "r.boolean()"
	case "json":
		return "r.raw_value()"
	case "list<string>":
		return "r.str_list()"
	case "map<string,json>":
		return "r.raw_map()"
	case "list<json>":
		return "raw_list(r)"
	case "map<string,string>":
		return "str_map(r)"
	}
	if elem := s.Repeated(f.Type); elem != "" {
		return "decode_list<" + elem + ">(r, decode_" + lower(elem) + ")"
	}
	return "decode_" + lower(f.Type) + "(r)"
}

func cppDerive(b *strings.Builder, s *Definition) {
	v := s.Vocab
	if v == nil {
		return
	}
	fmt.Fprintf(b, "\ninline const std::vector<std::string> k%sTerms = {", v.Name)
	for i, t := range v.Terms {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(b, "%q", t.Name)
	}
	b.WriteString("};\n")
	fmt.Fprintf(b, "inline const std::vector<std::string> k%sStripCritical = {", v.Name)
	first := true
	for _, t := range v.Terms {
		if !t.StripCritical {
			continue
		}
		if !first {
			b.WriteString(", ")
		}
		first = false
		fmt.Fprintf(b, "%q", t.Name)
	}
	b.WriteString("};\n")
	b.WriteString("\ninline bool holds(const std::vector<std::string>& v, const std::string& n) {\n")
	b.WriteString("    for (const auto& x : v) {\n        if (x == n) return true;\n    }\n    return false;\n}\n")
	if v.UsesMember() {
		b.WriteString(cppMember)
	}
	fmt.Fprintf(b, "\ninline void derive(const Reader& r, %s& v) {\n", v.Of)
	b.WriteString("    std::vector<std::string> kept;\n")
	fmt.Fprintf(b, "    for (const auto& name : v.%s) {\n", v.Critical)
	fmt.Fprintf(b, "        if (holds(k%sStripCritical, name)) continue;\n", v.Name)
	fmt.Fprintf(b, "        if (!holds(k%sTerms, name)) r.refuse(\"unknown_critical\");\n", v.Name)
	fmt.Fprintf(b, "        if (!holds(v.%s, name)) r.refuse(\"not_a_subset\");\n", v.Names)
	b.WriteString("        kept.push_back(name);\n    }\n")
	fmt.Fprintf(b, "    v.%s = kept;\n", v.Critical)
	for _, t := range v.Terms {
		fmt.Fprintf(b, "    if ((%s) != holds(v.%s, %q)) r.refuse(\"content_mismatch\");\n", cppTest(s, t), v.Names, t.Name)
	}
	b.WriteString("}\n")
}

func cppTest(s *Definition, t Term) string {
	if t.Always() {
		return "true"
	}
	e := "v"
	for _, f := range t.Path {
		e += "." + f.Ident("cpp")
	}
	switch {
	case t.Member != "":
		return fmt.Sprintf("member(%s, %q)", e, t.Member)
	case t.Membership():
		parts := make([]string, len(t.Is))
		for i, w := range t.Is {
			parts[i] = fmt.Sprintf("%s == %q", e, w)
		}
		return strings.Join(parts, " || ")
	}
	return cppPresent(s, t.Last(), e)
}

const cppMember = `
// [DEF-A8] Whether an opaque value is an object naming this member with
// something other than null. The key is decoded, so two spellings of one name
// are one name; the value is neither decoded nor judged.
inline bool member(const Raw& raw, const std::string& name) {
    Reader r{raw};
    try {
        r.skip_ws();
        if (r.at() != '{') return false;
        ++r.pos;
        r.skip_ws();
        while (r.at() == '"') {
            const std::string k = r.str();
            r.skip_ws();
            ++r.pos;
            r.skip_ws();
            if (k == name) return r.at() != 'n';
            r.skip_value();
            r.skip_ws();
            if (r.at() != ',') return false;
            ++r.pos;
            r.skip_ws();
        }
    } catch (const Refusal&) {
        return false;
    }
    return false;
}
`

func genCpp(s *Definition) string {
	var b strings.Builder
	esc := cppEscMinimal
	if s.Encoding.EscapeNonASCII() {
		esc = cppEscASCII
	}
	prelude := cppCommon + esc
	if s.StringMapDocument() {
		prelude += cppStrMap
	}
	if s.HasRepeated() {
		prelude += cppEncList
	}
	b.WriteString(strings.NewReplacer("@INDENT@", strconv.Itoa(s.Encoding.Indent)).Replace(prelude))
	cppVocabulary(&b, s)
	for _, st := range s.Structs {
		fmt.Fprintf(&b, "\nstruct %s {\n", st.Name)
		for _, f := range st.Fields {
			fmt.Fprintf(&b, "    %s %s%s;\n", cppType(s, f), f.Ident("cpp"), cppInit(f))
		}
		b.WriteString("};\n")
	}
	for _, st := range s.Structs {
		if !s.Envelope(st.Name) {
			cppEncoder(&b, s, st, false)
		}
	}
	tail := ""
	if s.Encoding.TrailingNewline() {
		tail = "    out += '\\n';\n"
	}
	if s.Document != "" {
		fmt.Fprintf(&b, "\ninline std::string encode(const %s& v) {\n    std::string out;\n    enc_%s(out, v, 0);\n%s    return out;\n}\n",
			s.Document, lower(s.Document), tail)
	}
	dup, skipSeen, skipKey, skipDup, strDup := "", "", "", "", ""
	if s.Encoding.RefuseDuplicateKeys() {
		dup, skipSeen, skipKey, skipDup = cppDupKeyRefuse, "        std::map<std::string, bool> seen;\n", "const std::string k = ", cppSkipDupKey
		strDup = cppStrDupKey
	}
	decode := cppDecodeCommon
	if s.HasStringMap() {
		decode += cppStrMapDecode
	}
	if s.HasRepeated() {
		decode += cppDecodeList
	}
	b.WriteString(strings.NewReplacer(
		"@DEPTH@", strconv.Itoa(s.Encoding.DepthLimit),
		"@DUPKEY@", dup,
		"@SKIPSEEN@", skipSeen,
		"@SKIPKEY@", skipKey,
		"@SKIPDUP@", skipDup,
		"@STRDUP@", strDup,
	).Replace(decode))
	if s.Timestamps() {
		b.WriteString(cppTimestamp)
	}
	cppWireHelpers(&b, s)
	cppDecoder(&b, s)
	cppProtocol(&b, s)
	b.WriteString("\n}  // namespace rec\n")
	return b.String()
}

func cppVocabulary(b *strings.Builder, s *Definition) {
	for _, en := range s.Enums {
		fmt.Fprintf(b, "\ninline const std::vector<std::string> k%sNames = {", en.Name)
		for i, m := range en.Members {
			if i > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(b, "%q", m.Name)
		}
		b.WriteString("};\n")
		fmt.Fprintf(b, "inline const std::string k%sUnknown = %q;\n", en.Name, en.Ann["unknown"])
		for _, key := range en.MemberAnn() {
			fmt.Fprintf(b, "inline const std::map<std::string, std::string> k%s%s = {\n", en.Name, exported(key))
			for _, m := range en.Members {
				if v, ok := m.Ann[key]; ok {
					fmt.Fprintf(b, "    {%q, %q},\n", m.Name, v)
				}
			}
			b.WriteString("};\n")
		}
	}
	for _, c := range s.Consts {
		if c.Type == "list<i32>" {
			fmt.Fprintf(b, "\ninline const std::vector<std::int32_t> k%s = {", exported(c.Name))
			for i, n := range c.Ints {
				if i > 0 {
					b.WriteString(", ")
				}
				fmt.Fprintf(b, "%d", n)
			}
		} else {
			fmt.Fprintf(b, "\ninline const std::vector<std::string> k%s = {", exported(c.Name))
			for i, v := range c.Strings {
				if i > 0 {
					b.WriteString(", ")
				}
				fmt.Fprintf(b, "%q", v)
			}
		}
		b.WriteString("};\n")
	}
}

func cppEncoder(b *strings.Builder, s *Definition, st Struct, flat bool) {
	p := plan(st)
	if flat {
		fmt.Fprintf(b, "\ninline void enc_wire_%s(std::string& out, const %s& v) {\n", lower(st.Name), st.Name)
	} else {
		fmt.Fprintf(b, "\ninline void enc_%s(std::string& out, const %s& v, int depth) {\n", lower(st.Name), st.Name)
	}
	b.WriteString("    out += '{';\n")
	if p.flag {
		b.WriteString("    bool first = true;\n")
	}
	for i, f := range st.Fields {
		e := "v." + f.Ident("cpp")
		ind := "    "
		if f.Omit != "never" {
			fmt.Fprintf(b, "    if (%s) {\n", cppPresent(s, f, e))
			ind = "        "
		}
		switch p.before[i] {
		case "always":
			fmt.Fprintf(b, "%sout += ',';\n", ind)
		case "flag":
			fmt.Fprintf(b, "%sif (!first) out += ',';\n", ind)
		}
		if p.clears(i) {
			fmt.Fprintf(b, "%sfirst = false;\n", ind)
		}
		if flat {
			fmt.Fprintf(b, "%sesc(out, %q);\n%sout += ':';\n", ind, f.Name, ind)
			fmt.Fprintf(b, "%s%s\n", ind, cppValueFlat(s, f, e))
		} else {
			fmt.Fprintf(b, "%sout += '\\n';\n%spad(out, depth + 1);\n", ind, ind)
			fmt.Fprintf(b, "%sesc(out, %q);\n%sout += \": \";\n", ind, f.Name, ind)
			fmt.Fprintf(b, "%s%s\n", ind, cppValue(s, f, e))
		}
		if f.Omit != "never" {
			b.WriteString("    }\n")
		}
	}
	if !flat {
		switch {
		case p.closeAlways:
			b.WriteString("    out += '\\n';\n    pad(out, depth);\n")
		case len(st.Fields) > 0:
			b.WriteString("    if (!first) { out += '\\n'; pad(out, depth); }\n")
		}
	}
	b.WriteString("    out += '}';\n}\n")
}

func cppType(s *Definition, f Field) string {
	switch f.Type {
	case "string":
		return "std::string"
	case "i32":
		return "std::int32_t"
	case "i64":
		return "std::int64_t"
	case "bool":
		return "bool"
	case "json":
		return "Raw"
	case "list<string>":
		return "std::vector<std::string>"
	case "map<string,json>":
		return "std::map<std::string, Raw>"
	case "list<json>":
		return "std::vector<Raw>"
	case "map<string,string>":
		return "std::map<std::string, std::string>"
	}
	if elem := s.Repeated(f.Type); elem != "" {
		return "std::vector<" + elem + ">"
	}
	if f.Omit == "absent" {
		return "std::optional<" + f.Type + ">"
	}
	return f.Type
}

func cppInit(f Field) string {
	switch f.Type {
	case "i32", "i64":
		return " = 0"
	case "bool":
		return " = false"
	}
	return ""
}

func cppPresent(s *Definition, f Field, e string) string {
	if f.Omit == "absent" && s.IsStruct(f.Type) {
		return e + ".has_value()"
	}
	switch f.Type {
	case "i32", "i64":
		return e + " != 0"
	case "bool":
		return e
	}
	return "!" + e + ".empty()"
}

func cppValue(s *Definition, f Field, e string) string {
	switch f.Type {
	case "string":
		return "esc(out, " + e + ");"
	case "i32", "i64":
		return "num(out, " + e + ");"
	case "bool":
		return "out += " + e + ` ? "true" : "false";`
	case "json":
		return "raw(out, " + e + ", depth + 1);"
	case "list<string>":
		return "strs(out, " + e + ", depth + 1);"
	case "map<string,json>":
		return "rawmap(out, " + e + ", depth + 1);"
	case "map<string,string>":
		return "strmap(out, " + e + ", depth + 1);"
	}
	if elem := s.Repeated(f.Type); elem != "" {
		return "enc_list<" + elem + ">(out, " + e + ", depth + 1, enc_" + lower(elem) + ");"
	}
	if f.Omit == "absent" {
		return "enc_" + lower(f.Type) + "(out, *" + e + ", depth + 1);"
	}
	return "enc_" + lower(f.Type) + "(out, " + e + ", depth + 1);"
}
