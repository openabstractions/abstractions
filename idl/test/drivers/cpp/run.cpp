#include <algorithm>
#include <cstdint>
#include <cstdio>
#include <filesystem>
#include <initializer_list>
#include <string>
#include <vector>

#include "rec.h"

static std::string cp(std::initializer_list<unsigned long> points) {
    std::string s;
    for (unsigned long c : points) {
        if (c < 0x80) {
            s += static_cast<char>(c);
        } else if (c < 0x800) {
            s += static_cast<char>(0xC0 | (c >> 6));
            s += static_cast<char>(0x80 | (c & 0x3F));
        } else if (c < 0x10000) {
            s += static_cast<char>(0xE0 | (c >> 12));
            s += static_cast<char>(0x80 | ((c >> 6) & 0x3F));
            s += static_cast<char>(0x80 | (c & 0x3F));
        } else {
            s += static_cast<char>(0xF0 | (c >> 18));
            s += static_cast<char>(0x80 | ((c >> 12) & 0x3F));
            s += static_cast<char>(0x80 | ((c >> 6) & 0x3F));
            s += static_cast<char>(0x80 | (c & 0x3F));
        }
    }
    return s;
}

static const std::string kTs = "2026-09-08T05:07:14.951609Z";

static rec::Record awkward() {
    const std::string by = "Ada L" + cp({0x016B}) + "velace <ada@" + cp({0x4F8B, 0x3048}) +
                           ".jp> & co " + cp({0x2702, 0xFE0F}) + " " + cp({0x1F9FF});
    const std::string err = "line1" + cp({0x0A}) + "line2" + cp({0x09}) + "tabbed" + cp({0x01}) +
                            "ctrl " + cp({0x22}) + "quoted" + cp({0x22}) + " back" + cp({0x5C}) +
                            "slash";
    rec::Record r;
    r.content = {"abstraction.job/base@1", "abstraction.job/intent@1",
                 "abstraction.job/envelope@1", "abstraction.job/step@1"};
    r.critical = {"abstraction.job/base@1"};
    r.id = "1787202430967-a752f9a9c2c77b123ffd";
    r.kind = "download";
    rec::Envelope envelope;
    envelope.schema = "nas.example/transfer@2";
    envelope.actions = {"cancel", "nas.example/transfer@2#re-mirror"};
    r.envelope = envelope;
    r.state = "pending";
    r.spec =
        "{\"artifact\":{\"bytes\":9223372036854775807,\"empty_obj\":{},\"empty_arr\":[]},"
        "\"note\":\"a<b&c>d\",\"nested\":{\"deep\":{\"x\":1.50,\"neg\":-0.0}}}";
    r.progress.done = 0;
    r.progress.total = 9223372036854775807LL;
    r.progress.updated_at = kTs;
    rec::Step step;
    step.name = "";
    step.ordinal = 1;
    step.of = 0;
    step.done = 0;
    step.total = INT64_MIN;
    r.progress.step = step;
    r.lease.owner = "";
    r.lease.epoch = 0;
    r.lease.expires_at = kTs;
    r.error = err;
    rec::Intent intent;
    intent.want = "cancel";
    intent.by = by;
    intent.at = kTs;
    r.intent = intent;
    r.extensions = {
        {"zz.example/v1", "{\"k\":\"v\"}"},
        {"aa.example/v1", "[1,2,3]"},
        {cp({0x00E9}) + ".example", "null"},
        {cp({0xFFFD}) + ".example", "true"},
        {cp({0x1D11E}) + ".example", "{}"},
    };
    r.created_at = kTs;
    r.updated_at = kTs;
    return r;
}

static rec::Record ranges() {
    rec::Record r;
    r.content = {"abstraction.job/base@1", "abstraction.download/ranges@1"};
    r.critical = {"abstraction.job/base@1"};
    r.id = "1787202430967-a752f9a9c2c77b123ffd";
    r.kind = "download";
    r.state = "running";
    r.spec = "{\"artifact\":{\"bytes\":23068672}}";
    r.checkpoint =
        "{\"verified_prefix\":4194304,\"verified\":"
        "[[0,4194304],[8388608,12582912],[20971520,23068672]]}";
    r.progress.done = 10485760;
    r.progress.total = 23068672;
    r.progress.updated_at = "2026-08-20T05:07:14.951609Z";
    r.lease.owner = "go-worker";
    r.lease.epoch = 2;
    r.lease.expires_at = "2026-08-20T05:08:14.635068Z";
    r.created_at = "2026-08-20T05:07:10.967343Z";
    r.updated_at = "2026-08-20T05:07:15.134811Z";
    return r;
}

static rec::Record terminal() {
    rec::Record r;
    r.content = {"abstraction.job/base@1", "abstraction.job/step@1",
                 "abstraction.job/terminal@1", "abstraction.job/recall@1"};
    r.critical = {"abstraction.job/base@1", "abstraction.job/terminal@1",
                  "abstraction.job/recall@1"};
    r.id = "1787202430967-a752f9a9c2c77b123ffd";
    r.kind = "download";
    r.state = "failed";
    r.spec = "{\"artifact\":{\"bytes\":64}}";
    r.checkpoint = "{\"verified_prefix\":8}";
    r.progress.done = 8;
    r.progress.total = 64;
    r.progress.updated_at = "2026-09-09T17:21:08.958178Z";
    rec::Step step;
    step.name = "fetch";
    step.ordinal = 1;
    step.of = 2;
    step.done = 8;
    step.total = 64;
    r.progress.step = step;
    r.lease.owner = "alpha";
    r.lease.epoch = 3;
    r.lease.expires_at = "2026-09-09T17:21:09.958178Z";
    rec::Recall recall;
    recall.reason = "yield";
    recall.by = "broker";
    recall.at = "2026-09-09T17:21:08.958178Z";
    recall.until = "2026-09-09T17:21:09.958178Z";
    r.lease.recall = recall;
    r.error = "source closed the connection";
    r.created_at = "2026-09-09T17:21:06.998457Z";
    r.updated_at = "2026-09-09T17:21:08.967883Z";
    return r;
}

// "wb", because Windows text mode turns every LF into CRLF and the definition
// declares LF.
static void write(const std::string& path, const std::string& body) {
    std::FILE* f = std::fopen(path.c_str(), "wb");
    std::fwrite(body.data(), 1, body.size(), f);
    std::fclose(f);
}

static std::string slurp(const std::string& path) {
    std::FILE* f = std::fopen(path.c_str(), "rb");
    std::string body;
    char chunk[4096];
    for (std::size_t n; (n = std::fread(chunk, 1, sizeof chunk, f)) > 0;) body.append(chunk, n);
    std::fclose(f);
    return body;
}

static std::string verdict(const std::string& data) {
    try {
        return "ok\t" + rec::decode(data).spec;
    } catch (const rec::Refusal& r) {
        return std::string(r.word) + "\t" + std::to_string(r.offset);
    }
}

int main(int argc, char** argv) {
    if (argc < 3) return 2;
    const std::string dir = argv[1];
    for (const auto& [name, r] : {std::pair{std::string("awkward"), awkward()},
                                  std::pair{std::string("ranges"), ranges()},
                                  std::pair{std::string("terminal"), terminal()}}) {
        const std::string encoded = rec::encode(r);
        write(dir + "/cpp-" + name + ".json", encoded);
        write(dir + "/cpp-rt-" + name + ".json", rec::encode(rec::decode(encoded)));
    }
    std::vector<std::string> names;
    for (const auto& e : std::filesystem::directory_iterator(argv[2])) {
        if (e.path().extension() == ".json") names.push_back(e.path().filename().string());
    }
    std::sort(names.begin(), names.end());
    std::string out;
    for (const std::string& n : names) {
        out += n.substr(0, n.size() - 5) + "\t" + verdict(slurp(std::string(argv[2]) + "/" + n)) + "\n";
    }
    write(dir + "/cpp-corpus.txt", out);
    return 0;
}
