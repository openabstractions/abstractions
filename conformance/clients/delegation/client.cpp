#include <abstraction/facade/jobs.hpp>
#include <abstraction/download/request/rec.h>
#include <algorithm>
#include <iostream>
#include <sstream>
#include <thread>

namespace f = abstraction::facade;
namespace d = abstraction::download::request;

void require(bool value) {
    if (!value) throw std::runtime_error("delegation assertion failed");
}

int main(int argc, char** argv) {
    try {
        if (argc == 2 && std::string(argv[1]) == "--help") {
            std::cout << "delegation_consumer submit|incapable endpoint key source-url; "
                         "recover|pending endpoint key epoch operation owner; "
                         "waiting|credential endpoint key source-url\n";
            return 0;
        }
        if (argc < 4) throw std::runtime_error("use --help");
        const std::string mode = argv[1];
        const auto guarantee = d::kDownstreamRecoveryGuarantees[0];
        const auto deadline = abstraction::ipc::Clock::now() + std::chrono::seconds(15);
        f::ResolutionClient resolver(argv[2]);

        if (mode == "incapable" && argc == 5) {
            try {
                f::resolve_job_operations(resolver, {guarantee}, abstraction::facade::Scope::Local, deadline);
                throw std::runtime_error("incapable provider resolved");
            } catch (const f::ResolutionError& e) {
                require(e.status == "unmet_requirements");
            }
            // Admission independently checks the submitted requirement.
            auto jobs = f::resolve_jobs(resolver, {}, abstraction::facade::Scope::Local, deadline);
            auto history = jobs.get_history_window();
            f::job_api::Submission submission;
            submission.identity = {argv[3], history.history_epoch};
            submission.kind = "download";
            d::Request request;
            request.sources.push_back({"http", argv[4]});
            auto raw = d::encode(request);
            submission.spec.assign(raw.begin(), raw.end());
            submission.required_guarantees = {guarantee};
            auto result = jobs.submit(submission);
            require(result.outcome == "definitely_not_accepted" && !result.receipt);
            const auto sealed = jobs.reconcile(submission.identity);
            require(sealed.outcome == "definitely_not_accepted" && !sealed.receipt);
            std::cout << "REFUSED " << wire_name(result.outcome) << "\n";
            return 0;
        }

        if (mode == "credential" && argc == 5) {
            // The runtime admits the credential hf and refuses applying it as
            // revoked: the operation fails permanently with cause credential and
            // the applier outcome. A name it does not hold is refused at
            // admission with no receipt (job JOB-A8, JOB-A16; download DL-K1).
            const auto credentials = d::kCredentialGuarantees[0];
            auto jobs = f::resolve_job_operations(resolver, {credentials}, abstraction::facade::Scope::Local, deadline);
            auto history = jobs.get_history_window();
            auto submission_for = [&](const std::string& key, const std::string& name) {
                f::job_api::Submission submission;
                submission.identity = {key, history.history_epoch};
                submission.kind = "download";
                d::Request request;
                d::Source source{"http", std::string(argv[4]) + "/" + name};
                source.credential = name;
                request.sources.push_back(source);
                auto raw = d::encode(request);
                submission.spec.assign(raw.begin(), raw.end());
                submission.required_guarantees = {credentials};
                return submission;
            };
            auto named = submission_for(argv[3], "hf");
            require(jobs.submit(named).outcome == "accepted");
            for (;;) {
                auto observed = jobs.observe_work(named.identity);
                require(observed.outcome == "observed" && observed.snapshot.has_value());
                if (observed.snapshot->state == "failed") {
                    require(observed.snapshot->failure.has_value());
                    const auto& failure = *observed.snapshot->failure;
                    require(failure.classification == f::job_api::FailureClass::Permanent);
                    require(failure.cause == f::job_api::kFailureCauseCredential);
                    require(failure.message == "download attempt failed: credential:revoked:hf");
                    break;
                }
                require(observed.snapshot->state == "pending" || observed.snapshot->state == "running");
                require(abstraction::ipc::Clock::now() < deadline);
                std::this_thread::sleep_for(std::chrono::milliseconds(20));
            }
            auto refused = jobs.submit(submission_for(std::string(argv[3]) + "-missing", "missing"));
            require(refused.outcome == "invalid" && refused.reason == "credential:unknown:missing" && !refused.receipt.has_value());
            std::cout << "CREDENTIAL revoked:hf REFUSED unknown:missing\n";
            return 0;
        }

        if (mode == "waiting" && argc == 5) {
            // A request with network unmetered requires the network-cost
            // guarantee, reads the runtime's waiting word, and is cancelled
            // with nothing fetched (download CONTRACT DL-N2 to DL-N6).
            const auto network = d::kNetworkCostGuarantees[0];
            auto jobs = f::resolve_job_operations(resolver, {network}, abstraction::facade::Scope::Local, deadline);
            auto history = jobs.get_history_window();
            f::job_api::Submission submission;
            submission.identity = {argv[3], history.history_epoch};
            submission.kind = "download";
            d::Request request;
            request.sources.push_back({"http", argv[4]});
            d::Constraints constraints;
            constraints.network = d::Network::Unmetered;
            request.constraints = constraints;
            auto raw = d::encode(request);
            submission.spec.assign(raw.begin(), raw.end());
            submission.required_guarantees = {network};
            auto result = jobs.submit(submission);
            require(result.outcome == "accepted" && result.receipt.has_value());
            for (;;) {
                auto observed = jobs.observe_work(submission.identity);
                require(observed.outcome == "observed" && observed.snapshot.has_value());
                // The word is written while the lease is held and the work
                // reads pending once the lease is released.
                if (observed.snapshot->waiting == "network:metered" && observed.snapshot->state == "pending") break;
                require(observed.snapshot->state == "pending" || observed.snapshot->state == "running");
                require(abstraction::ipc::Clock::now() < deadline);
                std::this_thread::sleep_for(std::chrono::milliseconds(20));
            }
            require(jobs.cancel_work(submission.identity).outcome == "requested");
            for (;;) {
                auto observed = jobs.observe_work(submission.identity);
                require(observed.outcome == "observed" && observed.snapshot.has_value());
                if (observed.snapshot->state == "cancelled") {
                    require(observed.snapshot->waiting.empty());
                    break;
                }
                require(abstraction::ipc::Clock::now() < deadline);
                std::this_thread::sleep_for(std::chrono::milliseconds(20));
            }
            std::cout << "WAITED network:metered CANCELLED\n";
            return 0;
        }

        auto jobs = f::resolve_job_operations(resolver, {guarantee}, abstraction::facade::Scope::Local, deadline);
        if (mode == "submit" && argc == 5) {
            auto history = jobs.get_history_window();
            f::job_api::Submission submission;
            submission.identity = {argv[3], history.history_epoch};
            submission.kind = "download";
            d::Request request;
            request.sources.push_back({"http", argv[4]});
            auto raw = d::encode(request);
            submission.spec.assign(raw.begin(), raw.end());
            submission.required_guarantees = {guarantee};
            auto result = jobs.submit(submission);
            require(result.outcome == "accepted" && result.receipt.has_value());
            const auto& r = *result.receipt;
            require(std::find(r.accepted_guarantees.begin(), r.accepted_guarantees.end(),
                              guarantee) != r.accepted_guarantees.end());
            std::cout << r.identity.history_epoch << " " << r.operation_id
                      << " " << r.logical_owner << "\n";
            return 0;
        }

        if ((mode == "recover" || mode == "pending") && argc == 7) {
            f::job_api::RequestIdentity id{argv[3], argv[4]};
            auto recovered = jobs.reconcile(id);
            require(recovered.outcome == "accepted" && recovered.receipt.has_value());
            require(recovered.receipt->operation_id == argv[5] &&
                    recovered.receipt->logical_owner == argv[6]);
            for (;;) {
                auto result = jobs.observe_work(id);
                require(result.outcome == "observed" && result.snapshot.has_value());
                require(result.snapshot->receipt.operation_id == argv[5]);
                if (mode == "pending") {
                    if (result.snapshot->state != "pending") {
                        throw std::runtime_error("incapable state=" + std::string(wire_name(result.snapshot->state)));
                    }
                    std::cout << "PENDING\n";
                    return 0;
                }
                if (result.snapshot->state == "complete") break;
                require(result.snapshot->state != "failed" && result.snapshot->state != "cancelled");
                require(abstraction::ipc::Clock::now() < deadline);
                std::this_thread::sleep_for(std::chrono::milliseconds(20));
            }
            std::ostringstream output;
            auto copied = jobs.copy_result(id, output);
            if (copied.error) std::rethrow_exception(copied.error);
            require(copied.confirmed == static_cast<std::int64_t>(output.str().size()));
            static const char hex[] = "0123456789abcdef";
            for (unsigned char b : output.str()) std::cout << hex[b >> 4] << hex[b & 15];
            std::cout << "\n";
            return 0;
        }
        throw std::runtime_error("invalid arguments");
    } catch (const std::exception& e) {
        std::cerr << e.what() << "\n";
        return 1;
    }
}
