// What the hand-written C++ peer makes of the corpus and of stored records.
//
//     verdicts <corpus dir> <record path or dir> ...
//
// One tab-separated line per input, read by scripts/peers/corpus.sh:
//
//     peer       cpp
//     toolchain  msvc 19.51.36231 / libstdc++ 15.2.0 — whichever built this
//     verdict    <fixture>  ok | refused | unknown-model | escaped
//     roundtrip  <fixture>  same | differs          (accepted fixtures only)
//     wire       <record>   read | moved | refused
//
// The toolchain line is the point of running this twice. The same source built
// by two compilers reads two different sets of stored records, because the
// range of instants a record may spell is the standard library's and not ours.

#include <abstraction/job/record.h>

#include <algorithm>
#include <cstdio>
#include <filesystem>
#include <fstream>
#include <sstream>
#include <string>
#include <vector>

namespace fs = std::filesystem;
using namespace abstraction::job;

static std::string slurp(const fs::path& p) {
    std::ifstream in(p, std::ios::binary);
    std::ostringstream all;
    all << in.rdbuf();
    return all.str();
}

static std::vector<fs::path> jsons(const fs::path& path) {
    std::vector<fs::path> out;
    if (!fs::is_directory(path)) {
        out.push_back(path);
        return out;
    }
    for (const auto& e : fs::directory_iterator(path)) {
        if (e.path().extension() == ".json") out.push_back(e.path());
    }
    std::sort(out.begin(), out.end());
    return out;
}

// A fixture whose canonical form is not its own bytes says so in a sibling file:
// `<fixture>.roundtrip` holds what a decode and re-encode must produce. That is
// how a payload's insignificant whitespace is tested — [JOB-E7] deliberately does
// not carry it, so the input and the expectation are different bytes on purpose.
static std::string wanted(const fs::path& p, const std::string& raw) {
    const fs::path expectation = p.string() + ".roundtrip";
    return fs::exists(expectation) ? slurp(expectation) : raw;
}

static std::string toolchain() {
    char buf[128];
#if defined(_MSC_VER)
    std::snprintf(buf, sizeof buf, "msvc %d.%d.%d", _MSC_VER / 100, _MSC_VER % 100,
                  _MSC_FULL_VER % 100000);
#elif defined(__clang__)
    std::snprintf(buf, sizeof buf, "clang %d.%d.%d", __clang_major__, __clang_minor__,
                  __clang_patchlevel__);
#elif defined(__GNUC__)
    std::snprintf(buf, sizeof buf, "g++ %d.%d.%d", __GNUC__, __GNUC_MINOR__,
                  __GNUC_PATCHLEVEL__);
#else
    std::snprintf(buf, sizeof buf, "unknown");
#endif
    return buf;
}

// "escaped" is not a verdict this layer offers. A refusal that leaves
// Record::decode as anything but a JobError is one no caller can catch, so it
// is counted apart from a refusal rather than folded into it.
static const char* verdict(const std::string& text, Record* out) {
    try {
        *out = Record::decode(text);
        return "ok";
    } catch (const UnknownSchema&) {
        return "unknown-model";
    } catch (const JobError&) {
        return "refused";
    } catch (const std::exception&) {
        return "escaped";
    }
}

int main(int argc, char** argv) {
    std::setvbuf(stdout, nullptr, _IONBF, 0);
    if (argc < 3) {
        std::fprintf(stderr, "usage: verdicts <corpus dir> <record path or dir> ...\n");
        return 2;
    }

    std::printf("peer\tcpp\ntoolchain\t%s\n", toolchain().c_str());

    for (const fs::path& p : jsons(argv[1])) {
        const std::string name = p.filename().string();
        const std::string raw = slurp(p);
        Record record;
        const char* word = verdict(raw, &record);
        std::printf("verdict\t%s\t%s\n", name.c_str(), word);
        if (std::string(word) != "ok") continue;
        std::printf("roundtrip\t%s\t%s\n", name.c_str(),
                    record.encode() == wanted(p, raw) ? "same" : "differs");
    }

    for (int i = 2; i < argc; ++i) {
        for (const fs::path& p : jsons(argv[i])) {
            const std::string name = p.filename().string();
            const std::string raw = slurp(p);
            try {
                const Record record = Record::decode(raw);
                std::printf("wire\t%s\t%s\n", name.c_str(),
                            record.encode() == raw ? "read" : "moved");
            } catch (const std::exception& e) {
                std::printf("wire\t%s\trefused\t%s\n", name.c_str(), e.what());
            }
        }
    }
    return 0;
}
