#include <abstraction/facade/client.hpp>
#include <algorithm>
#include <cctype>
#include <iostream>
#include <stdexcept>

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
        machine.Log().Log(0, "facade to Go service\nsecond line \xE2\x98\x83", {{"binding", "facade"}, {"empty", ""}});
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
        abstraction::ipc::FrameTransport transport(abstraction::router::default_endpoint(), 10000, 1 << 20);
        const auto reply = transport.ExchangeFrame(
            R"({"version":1,"service":"abstraction.router/router@1","method":"Missing","arguments":{}})");
        bool typed_refusal = false;
        try { abstraction::router::service_response(reply, "abstraction.router/router@1", "Missing"); }
        catch (const abstraction::router::ServiceError& error) { typed_refusal = error.code == "unknown_method"; }
        require(typed_refusal && machine.Router().Hosts().asked.size() == 2,
                "typed unknown-method refusal changed or reached the provider");
        std::cout << "CLIENT VERIFIED: three service calls and provider audit\n" << std::flush;
        // The harness observes the service-owned log before letting this caller
        // exit. One-way logging itself does not promise a persistence receipt.
        require(std::cin.get() == '\n', "harness did not observe the logging provider");
    } catch (const std::exception& e) {
        std::cerr << e.what() << '\n'; return 1;
    }
}
