// Time N identical caller@1 Observe calls from one C++ process at a runtime
// resolver endpoint through the shared abstraction_ipc FrameTransport and the
// generated facade client: single opens a connection per call, session keeps
// one (oa_ipc_session_call).
//
//   cpp_percall <runtime endpoint> <calls> <warmup> single|session
//
// Prints one PERCALL line; see percall/README.md.
#include <abstraction/facade/rec.h>
#include <abstraction/ipc/frame.hpp>
#include <algorithm>
#include <chrono>
#include <cmath>
#include <cstdio>
#include <exception>
#include <string>
#include <vector>

int main(int argc, char** argv) {
    if (argc != 5 || (std::string(argv[4]) != "single" && std::string(argv[4]) != "session")) {
        std::printf("cpp_percall <runtime endpoint> <calls> <warmup> single|session\n");
        return argc == 2 && std::string(argv[1]) == "--help" ? 0 : 2;
    }
    try {
        const int calls = std::stoi(argv[2]), warmup = std::stoi(argv[3]);
        if (calls < 1 || warmup < 0) return 2;
        const bool session = std::string(argv[4]) == "session";
        auto transport = abstraction::ipc::FrameTransport(argv[1], 10000).with_sessions(session);
        abstraction::facade::CallerClient<abstraction::ipc::FrameTransport> client(transport);
        std::vector<double> samples;
        double first = 0;
        std::string code;
        for (int i = 0; i < warmup + calls; ++i) {
            const auto began = std::chrono::steady_clock::now();
            const auto observed = client.observe();
            const double took = std::chrono::duration<double, std::milli>(std::chrono::steady_clock::now() - began).count();
            if (observed.outcome != abstraction::facade::CallerOutcome::Observed) {
                std::fprintf(stderr, "observe outcome is not observed\n");
                return 1;
            }
            if (i == 0) {
                first = took;
                for (const auto& a : observed.attributes)
                    if (a.attribute == "code") code = a.proof;
            }
            if (i >= warmup) samples.push_back(took);
        }
        std::sort(samples.begin(), samples.end());
        auto rank = [&](double p) {
            auto i = static_cast<long>(std::ceil(samples.size() * p)) - 1;
            return samples[static_cast<size_t>(std::max(0L, i))];
        };
        std::printf("PERCALL %s calls=%d first=%.3f p50=%.3f p90=%.3f p99=%.3f max=%.3f code=%s\n",
                    session ? "cpp-session" : "cpp", static_cast<int>(samples.size()), first, rank(0.5), rank(0.9), rank(0.99), samples.back(), code.c_str());
        return 0;
    } catch (const std::exception& e) {
        std::fprintf(stderr, "cpp_percall: %s\n", e.what());
        return 1;
    }
}
