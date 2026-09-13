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
                         "recover|pending endpoint key epoch operation owner\n";
            return 0;
        }
        if (argc < 4) throw std::runtime_error("use --help");
        const std::string mode = argv[1];
        const auto guarantee = d::kDownstreamRecoveryGuarantees[0];
        const auto deadline = abstraction::ipc::Clock::now() + std::chrono::seconds(15);
        f::ResolutionClient resolver(argv[2]);

        if (mode == "incapable" && argc == 5) {
            try {
                f::ResolveJobOperations(resolver, {guarantee}, "local", deadline);
                throw std::runtime_error("incapable provider resolved");
            } catch (const f::ResolutionError& e) {
                require(e.status == "unmet_requirements");
            }
            // Admission independently checks the submitted requirement.
            auto jobs = f::ResolveJobs(resolver, {}, "local", deadline);
            auto history = jobs.GetHistoryWindow();
            f::job_api::Submission submission;
            submission.identity = {argv[3], history.history_epoch};
            submission.kind = "download";
            d::Request request;
            request.sources.push_back({"http", argv[4]});
            auto raw = d::encode(request);
            submission.spec.assign(raw.begin(), raw.end());
            submission.required_guarantees = {guarantee};
            auto result = jobs.Submit(submission);
            require(result.outcome == "definitely_not_accepted" && !result.receipt);
            const auto sealed = jobs.Reconcile(submission.identity);
            require(sealed.outcome == "definitely_not_accepted" && !sealed.receipt);
            std::cout << "REFUSED " << result.outcome << "\n";
            return 0;
        }

        auto jobs = f::ResolveJobOperations(resolver, {guarantee}, "local", deadline);
        if (mode == "submit" && argc == 5) {
            auto history = jobs.GetHistoryWindow();
            f::job_api::Submission submission;
            submission.identity = {argv[3], history.history_epoch};
            submission.kind = "download";
            d::Request request;
            request.sources.push_back({"http", argv[4]});
            auto raw = d::encode(request);
            submission.spec.assign(raw.begin(), raw.end());
            submission.required_guarantees = {guarantee};
            auto result = jobs.Submit(submission);
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
            auto recovered = jobs.Reconcile(id);
            require(recovered.outcome == "accepted" && recovered.receipt.has_value());
            require(recovered.receipt->operation_id == argv[5] &&
                    recovered.receipt->logical_owner == argv[6]);
            for (;;) {
                auto result = jobs.ObserveWork(id);
                require(result.outcome == "observed" && result.snapshot.has_value());
                require(result.snapshot->receipt.operation_id == argv[5]);
                if (mode == "pending") {
                    if (result.snapshot->state != "pending") {
                        throw std::runtime_error("incapable state=" + result.snapshot->state);
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
            auto copied = jobs.CopyResult(id, output);
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
