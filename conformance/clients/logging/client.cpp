#include <abstraction/logging/client.hpp>
#include <iostream>

int main(int argc, char** argv) {
    if (argc != 2) return 2;
    // This capability fixture supplies its provider explicitly. The separate
    // facade fixture proves runtime resolution.
    auto client = abstraction::logging::Logger(abstraction::logging::default_endpoint());
    const std::string mode = argv[1];
    if (mode == "write") {
        client.Log(0, "from cpp\nsecond line \xE2\x98\x83", {{"language", "cpp"}, {"empty", ""}});
    } else if (mode == "bad-schema") {
        abstraction::logging::Record record;
        record.schema = 2;
        record.time = "2026-09-11T12:00:00.000000Z";
        record.msg = "must not be written";
        bool refused = false;
        try { client.Write(record); } catch (const std::exception&) { refused = true; }
        if (!refused) return 1;
    } else if (mode == "absent") {
        bool refused = false;
        try { client.Log(0, "must not create local state"); } catch (const std::exception&) { refused = true; }
        if (!refused) return 1;
    } else return 2;
    std::cout << "PASS: C++ " << mode << '\n';
}
