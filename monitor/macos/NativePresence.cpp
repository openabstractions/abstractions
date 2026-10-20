#include "NativePresence.h"
#include <abstraction/facade/resolution.hpp>
#include <abstraction/facade/applications/rec.h>
#include <atomic>
#include <cstring>
#include <memory>
#include <mutex>

namespace {
namespace facade = abstraction::facade;
namespace applications = abstraction::facade::applications;
namespace ipc = abstraction::ipc;
constexpr char application[] = "openabstractions-panel";
constexpr std::uint32_t budget = 5000;
constexpr std::uint32_t frame_limit = 1 << 20;
std::string fresh_key() {
    static std::atomic<std::uint64_t> serial{0};
    return std::to_string(ipc::Clock::now().time_since_epoch().count()) + "-" + std::to_string(++serial);
}
}

struct oa_panel_presence {
    std::mutex mutex;
    ipc::FrameTransport transport;
    std::string instance;
    bool retired = false;
    explicit oa_panel_presence(ipc::FrameTransport value) : transport(std::move(value)) {}
};

extern "C" oa_panel_presence *oa_panel_presence_create(void) {
    try {
        const auto deadline = ipc::Clock::now() + std::chrono::milliseconds(budget);
        // Independent installed selection and generated resolution both run in
        // CFBundleExecutable. The Go child's connection supplies no proof here.
        const auto expected = ipc::select_runtime(deadline);
        auto resolver = facade::ResolutionClient(facade::runtime_endpoint(), budget)
                            .with_server_expectation(expected);
        facade::ResolveRequest request;
        request.capability = "abstraction.facade";
        request.contracts = {"abstraction.facade/applications@3"};
        request.scope = facade::Scope::Local;
        const auto reference = resolver.bind_local(request, deadline);
        auto transport = ipc::FrameTransport(reference.endpoint, budget, frame_limit)
                             .with_server_expectation(expected);
        return new oa_panel_presence(std::move(transport));
    } catch (...) { return nullptr; }
}

extern "C" int oa_panel_presence_announce(oa_panel_presence *owner, char *instance, size_t capacity) {
    if (!owner || !instance || capacity < 129) return 1;
    instance[0] = 0;
    std::lock_guard<std::mutex> lock(owner->mutex);
    if (owner->retired) return 1;
    try {
        facade::ApplicationPresence presence;
        presence.application = application;
        presence.instance = owner->instance;
        presence.lease_ms = 30000;
        applications::ApplicationsClient<ipc::FrameTransport> client(owner->transport);
        const auto change = client.announce(fresh_key(), presence);
        if (change.outcome != facade::ApplicationOutcome::Applied || change.instance.empty() ||
            change.instance.size() > 128 || (!owner->instance.empty() && change.instance != owner->instance)) {
            owner->retired = true;
            return 1;
        }
        owner->instance = change.instance;
        std::memcpy(instance, owner->instance.data(), owner->instance.size());
        instance[owner->instance.size()] = 0;
        return 0;
    } catch (...) {
        // A sent Announce may have been accepted. Never mint another instance
        // or resend after uncertainty; expiry owns eventual presence removal.
        owner->retired = true;
        return 2;
    }
}

extern "C" void oa_panel_presence_close(oa_panel_presence *owner) {
    if (!owner) return;
    {
        std::lock_guard<std::mutex> lock(owner->mutex);
        if (!owner->instance.empty()) {
            try {
                applications::ApplicationsClient<ipc::FrameTransport> client(owner->transport);
                (void)client.withdraw(application, owner->instance);
            } catch (...) { /* An unavailable withdrawal leaves lease expiry. */ }
        }
        owner->retired = true;
    }
    delete owner;
}
