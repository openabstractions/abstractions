// One unchanged installed application for every Linux lifecycle scenario.
// It links only the installed facade jobs binding, shared IPC and the portable
// download request vocabulary. It names no provider, private path, daemon or
// endpoint: the default runtime bootstrap and installation-selected trust find
// the service. The fixture supplies only a source URL, the expected artifact
// size and digest, and the caller-retained request identity.
#include <abstraction/facade/jobs.hpp>
#include <abstraction/download/request/rec.h>
#include <abstraction/ipc/frame.hpp>
#include <chrono>
#include <cstdlib>
#include <exception>
#include <iostream>
#include <sstream>
#include <stdexcept>
#include <string>
#include <thread>
#include <vector>

namespace f = abstraction::facade;
using Clock = abstraction::ipc::Clock;
static const std::vector<std::string> kReconciliation{"abstraction.job/reconciliation@1"};

static f::JobsClient Bind(Clock::time_point deadline, std::vector<std::string> guarantees = kReconciliation) {
    return f::resolve_job_operations(f::ResolutionClient(), std::move(guarantees), abstraction::facade::Scope::Local, deadline);
}

static int Report(const char* label, const f::job_api::AcceptanceResult& result) {
    if (result.outcome == "accepted" && result.receipt) {
        const auto& r = *result.receipt;
        std::cout << label << ' ' << r.operation_id << ' ' << r.logical_owner << ' '
                  << r.identity.history_epoch << ' ' << r.identity.key << '\n';
        return 0;
    }
    std::cout << "NOT_ACCEPTED " << wire_name(result.outcome) << ' ' << result.reason << '\n';
    return 0;
}

// The typed refusal: the resolution status, then its cause. A transport cause
// is the IPC status word; its message is the reason, printed last because it
// is free text.
static void Refused(const f::ResolutionError& e) {
    std::string cause = "none", reason;
    if (e.cause) {
        try {
            std::rethrow_exception(e.cause);
        } catch (const abstraction::ipc::FrameError& frame) {
            cause = abstraction::ipc::status_name(frame.status);
            reason = frame.what();
        } catch (const std::exception& other) {
            cause = "exception";
            reason = other.what();
        }
    }
    std::cout << "REFUSED resolution " << e.status << " cause " << cause;
    if (!reason.empty()) std::cout << " reason " << reason;
    std::cout << '\n';
}

static std::chrono::seconds Seconds(const char* text) {
    return std::chrono::seconds(std::strtol(text, nullptr, 10));
}

int main(int argc, char** argv) {
    try {
        const std::string mode = argc > 1 ? argv[1] : "--help";
        if (mode == "--help") {
            std::cout << "lifecycle_consumer absent\n"
                         "lifecycle_consumer resolve\n"
                         "lifecycle_consumer refuse-guarantee GUARANTEE\n"
                         "lifecycle_consumer submit URL SIZE DIGEST KEY\n"
                         "lifecycle_consumer observe KEY EPOCH\n"
                         "lifecycle_consumer result KEY EPOCH OPERATION SECONDS\n";
            return 0;
        }
        if (mode == "absent" && argc == 2) {
            try {
                auto jobs = Bind(Clock::now() + std::chrono::seconds(5));
                const auto history = jobs.get_history_window();
                std::cout << "UNEXPECTED resolved " << history.logical_owner << '\n';
                return 3;
            } catch (const abstraction::ipc::FrameError& e) {
                std::cout << "REFUSED frame " << static_cast<int>(e.status) << " cause "
                          << abstraction::ipc::status_name(e.status) << " reason " << e.what() << '\n';
            } catch (const f::ResolutionError& e) {
                Refused(e);
            }
            return 0;
        }
        if (mode == "resolve" && argc == 2) {
            auto jobs = Bind(Clock::now() + std::chrono::seconds(10));
            const auto history = jobs.get_history_window();
            std::cout << "RESOLVED " << history.logical_owner << ' ' << history.history_epoch << '\n';
            return 0;
        }
        if (mode == "refuse-guarantee" && argc == 3) {
            try {
                Bind(Clock::now() + std::chrono::seconds(10), {argv[2]}).get_history_window();
                std::cout << "UNEXPECTED resolved\n";
                return 3;
            } catch (const f::ResolutionError& e) {
                Refused(e);
            }
            return 0;
        }
        if (mode == "submit" && argc == 6) {
            auto jobs = Bind(Clock::now() + std::chrono::seconds(15));
            const auto history = jobs.get_history_window();
            abstraction::download::request::Request request;
            request.artifact.size = std::strtoll(argv[3], nullptr, 10);
            if (std::string(argv[4]) != "-") request.artifact.digest = argv[4];
            const std::string url = argv[2];
            request.sources.push_back({url.rfind("https:", 0) == 0 ? "https" : "http", url});
            const auto payload = abstraction::download::request::encode(request);
            f::job_api::Submission submission;
            submission.identity = {argv[5], history.history_epoch};
            submission.kind = "download";
            submission.spec.assign(payload.begin(), payload.end());
            submission.required_guarantees = kReconciliation;
            // The identity is retained before Submit so an unknown outcome is recoverable.
            std::cout << "IDENTITY " << submission.identity.key << ' ' << submission.identity.history_epoch << '\n' << std::flush;
            try {
                return Report("ACCEPTED", jobs.submit(submission));
            } catch (const abstraction::ipc::FrameError& e) {
                std::cout << "UNKNOWN frame " << static_cast<int>(e.status) << '\n' << std::flush;
                return Report("RECONCILED", Bind(Clock::now() + std::chrono::seconds(15)).reconcile(submission.identity));
            }
        }
        if (mode == "observe" && argc == 4) {
            auto jobs = Bind(Clock::now() + std::chrono::seconds(10));
            const auto observed = jobs.observe_work({argv[2], argv[3]});
            if (observed.outcome != "observed" || !observed.snapshot) {
                std::cout << "OBSERVATION " << wire_name(observed.outcome) << '\n';
                return 0;
            }
            const auto& s = *observed.snapshot;
            std::cout << "STATE " << wire_name(s.state) << ' ' << s.progress.done << ' ' << s.progress.total << ' '
                      << s.receipt.operation_id << '\n';
            return 0;
        }
        if (mode == "result" && argc == 6) {
            const auto deadline = Clock::now() + Seconds(argv[5]);
            auto jobs = Bind(deadline);
            const f::job_api::RequestIdentity identity{argv[2], argv[3]};
            const auto recovered = jobs.reconcile(identity);
            if (recovered.outcome != "accepted" || !recovered.receipt) return Report("RECONCILED", recovered), 4;
            if (recovered.receipt->operation_id != argv[4]) throw std::runtime_error("receipt changed");
            Report("RECONCILED", recovered);
            for (;;) {
                const auto observed = jobs.observe_work(identity);
                if (observed.outcome != "observed" || !observed.snapshot) throw std::runtime_error("operation unobservable: " + std::string(wire_name(observed.outcome)));
                const auto& s = *observed.snapshot;
                if (s.receipt.operation_id != argv[4]) throw std::runtime_error("operation changed");
                if (s.state == "complete") break;
                if (s.state == "failed" || s.state == "cancelled") {
                    std::cout << "FAILED " << wire_name(s.state) << ' ' << (s.failure ? s.failure->message : "") << '\n';
                    return 5;
                }
                if (Clock::now() >= deadline) throw std::runtime_error("observation budget expired");
                std::this_thread::sleep_for(std::chrono::milliseconds(100));
            }
            std::ostringstream output;
            const auto copy = jobs.copy_result(identity, output);
            if (copy.error) std::rethrow_exception(copy.error);
            const auto bytes = output.str();
            if (copy.confirmed != static_cast<std::int64_t>(bytes.size())) throw std::runtime_error("copy count differs");
            static const char digits[] = "0123456789abcdef";
            std::cout << "RESULT " << bytes.size() << ' ';
            for (unsigned char byte : bytes) std::cout << digits[byte >> 4] << digits[byte & 15];
            std::cout << '\n';
            if (!std::cout) throw std::runtime_error("result output failed");
            return 0;
        }
        std::cerr << "unknown mode; use --help\n";
        return 2;
    } catch (const abstraction::ipc::FrameError& e) {
        std::cout << "ERROR frame " << static_cast<int>(e.status) << ' ' << e.what() << '\n';
        return 6;
    } catch (const std::exception& e) {
        std::cout << "ERROR " << e.what() << '\n';
        return 7;
    }
}
