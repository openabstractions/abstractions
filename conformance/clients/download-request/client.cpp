#include <abstraction/download/request/rec.h>
#include <fstream>
#include <filesystem>
#include <limits>
#include <stdexcept>
namespace api = abstraction::download::request;
int main(int argc, char** argv) {
    if (argc != 2) return 2;
    auto write = [&](const char* name, const api::Request& request) {
        auto bytes = api::encode(request);
        std::ofstream file(std::filesystem::path(argv[1]) / name, std::ios::binary);
        file.write(bytes.data(), static_cast<std::streamsize>(bytes.size()));
        if (!file) throw std::runtime_error("request output failed");
    };
    write("http.json", {{"sha256:0123456789abcdef", 123456789},
        {{"https", "https://example.invalid/archive?x=1&y=2"}}});
    write("empty.json", {});
    write("strings.json", {{"", std::numeric_limits<std::int64_t>::max()},
        {{"codec", std::string("quote\" slash\\ line\n nul\0", 24) + "\xF0\x9F\x8C\x8D"},
         {"https", "https://example.invalid/second"}}});
}
