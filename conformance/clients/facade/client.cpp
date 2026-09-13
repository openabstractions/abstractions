#include <abstraction/facade/client.hpp>
#include <algorithm>
#include <cctype>
#include <iostream>
#include <stdexcept>
#include <thread>

void require(bool condition, const char* message) {
    if (!condition) throw std::runtime_error(message);
}
template<class Action> void absent(Action action) {
    bool failed = false;
    try { action(); } catch (const std::exception&) { failed = true; }
    require(failed, "absent service returned success or a fallback value");
}
void caller(const abstraction::router::Observation& value) {
    auto path = value.caller.path_description;
    std::transform(path.begin(), path.end(), path.begin(), [](unsigned char c) { return char(std::tolower(c)); });
    require(!value.caller.user_description.empty() && path.find("facade_consumer") != std::string::npos,
            "router did not identify the C++ facade caller");
}
int main(int argc, char** argv) {
    try {
        if (argc != 2) return 2;
        const std::string mode = argv[1];
        auto machine = abstraction::facade::Discover();
        if (mode == "absent") {
            absent([&] { machine.Log().Log(0, "must not create local storage"); });
            absent([&] { machine.Config().Read(); });
            absent([&] { machine.ResolveLogReader().Read(""); });
            absent([&] { machine.ResolveConfigEditor().ReadUser(); });
            absent([&] { machine.Router().Models(); });
            absent([&] { machine.Router().Hosts(); });
            absent([&] { abstraction::router::PickRequest r; r.model = "qwen2.5"; machine.Router().Pick(r); });
            std::cout << "PASS: all facade services absent, no fallback\n";
            return 0;
        }
        const auto config = machine.Config().Read();
        require(config.store == (mode == "changed" ? "changed by service owner" : "service-owned store"),
                "facade did not read the service provider's configuration");
        require(config.origins.store.rung == "user" && !config.origins.store.path.empty(),
                "configuration provenance missing");
        require(config.off.at("test-tier") == "disabled by service owner", "configuration map changed");
        require(!config.stamp.empty(), "configuration stamp missing");
        if (mode == "changed") {
            std::cout << "PASS: changed service-owned configuration reached fresh facade client\n";
            return 0;
        }
        require(mode == "all", "unknown mode");
        auto editor = machine.ResolveConfigEditor();
        const auto original = editor.ReadUser();
        require(original.values.store == "service-owned store", "editor copied run or machine values");
        auto updated = original.values;
        updated.off["editor-proof"] = "temporary test setting";
        const auto applied = editor.ReplaceUser(original.revision, updated);
        require(applied.outcome == "applied",
                "revision-checked edit was not applied");
        const auto conflict = editor.ReplaceUser(original.revision, original.values);
        require(conflict.outcome == "conflict",
                "stale edit silently overwrote settings");
        require(machine.Config().Read().off.at("editor-proof") == "temporary test setting",
                "reader and editor disagree about service-owned settings");
        const auto restored = editor.ReplaceUser(applied.snapshot.revision, original.values);
        require(restored.outcome == "applied",
                "failed to restore fixture settings");
        machine.Log().Log(0, "facade to Go service\nsecond line \xE2\x98\x83", {{"binding", "facade"}, {"empty", ""}});
        const auto deadline=abstraction::ipc::Clock::now()+std::chrono::seconds(2);
        auto history=machine.ResolveLogReader({},"local",deadline);
        abstraction::logging::Page page;
        do {
            page=history.Read("",1,65536);
            require(page.outcome=="page","history refused");
            if(!page.records.empty()) break;
            std::this_thread::sleep_for(std::chrono::milliseconds(10));
        } while(abstraction::ipc::Clock::now()<deadline);
        require(page.records.size()==1 && page.records[0].msg=="facade to Go service\nsecond line \xE2\x98\x83" && page.records[0].attrs.at("binding")=="facade","history changed logged record");
        require(!page.records[0].identity.empty() && page.records[0].identity.back().verified,"history lost service attestation");
        const auto continuation=history.Read(page.next,1,65536);
        require(continuation.records.empty() && continuation.at_end && continuation.next==page.next,"history end replayed records");
        require(history.Read("expired:0").outcome=="gap","history silently restarted old cursor");
        require(history.Read("",1,1).outcome=="record_too_large","history silently skipped oversized record");
        const auto models = machine.Router().Models(); caller(models.observation);
        const auto hosts = machine.Router().Hosts(); caller(hosts.observation);
        require(models.models.empty() && hosts.hosts.empty() && hosts.asked.empty() && hosts.doubled.empty(),
                "empty provider inventory changed");
        abstraction::router::PickRequest request; request.model = "";
        const auto invalid = machine.Router().Pick(request); caller(invalid.observation);
        require(invalid.decision.verdict == "unparseable" && invalid.decision.endpoint.empty(),
                "typed invalid-model decision changed");
        request.model = "qwen2.5";
        request.allowed.emplace();
        const auto unavailable = machine.Router().Pick(request); caller(unavailable.observation);
        require(unavailable.decision.verdict == "not-here" && unavailable.decision.endpoint.empty() &&
                unavailable.decision.authorised.has_value() && unavailable.decision.authorised->hosts.empty(),
                "empty provider/allowance changed into service absence or unrestricted permission");
        const auto audited = machine.Router().Hosts(); caller(audited.observation);
        require(audited.asked.size() == 2, "router provider did not retain both decisions");
        for (const auto& a : audited.asked)
            require(a.caller == audited.observation.caller.path_description && a.user == audited.observation.caller.user_description,
                    "router provider audit is not bound to the client");
        std::cout << "CLIENT VERIFIED: service reads, revision-checked edits and provider audit\n" << std::flush;
        // The harness observes the service-owned log before letting this caller
        // exit. One-way logging itself does not promise a persistence receipt.
        require(std::cin.get() == '\n', "harness did not observe the logging provider");
    } catch (const std::exception& e) {
        std::cerr << e.what() << '\n'; return 1;
    }
}
