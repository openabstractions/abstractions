#include <algorithm>
#include <cstdio>
#include <filesystem>
#include <string>
#include <vector>

#include "rec.h"

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

int main(int argc, char** argv) {
    if (argc < 3) return 2;
    const std::string dir = argv[1];
    std::vector<std::string> names;
    for (const auto& e : std::filesystem::directory_iterator(argv[2])) {
        if (e.path().extension() == ".json") names.push_back(e.path().filename().string());
    }
    std::sort(names.begin(), names.end());
    std::string out;
    for (const std::string& n : names) {
        const std::string stem = n.substr(0, n.size() - 5);
        const std::string data = slurp(std::string(argv[2]) + "/" + n);
        try {
            const auto v = rec::decode(data);
            out += stem + "\tok\n";
            write(dir + "/cpp-rt-" + stem + ".json", rec::encode(v));
        } catch (const rec::Refusal& r) {
            out += stem + "\t" + std::string(r.word) + "\t" + std::to_string(r.offset) + "\n";
        }
    }
    write(dir + "/cpp-corpus.txt", out);
    return 0;
}
