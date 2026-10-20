#include "NativeHost.h"
#include <abstraction/facade/applications/rec.h>
#include <abstraction/facade/registration/rec.h>
#include <string>

extern "C" oa_appmodel_status oa_panel_appmodel_configure(oa_appmodel_session* session, const char* data, size_t size) {
    namespace registration = abstraction::facade::registration;
    try {
        const auto manifest = registration::from_json<registration::Manifest>(std::string_view(data, size));
        if (manifest.name != "openabstractions-panel") return OA_APPMODEL_INVALID;
        oa_appmodel_error error{};
        uint64_t callback = 0;
        for (const auto& verb : manifest.verbs) {
            const auto record = registration::to_json(verb);
            const auto status = oa_appmodel_session_activation(session,
                {manifest.name.data(), manifest.name.size()}, {record.data(), record.size()}, ++callback, &error);
            if (status != OA_APPMODEL_OK) return status;
        }
        return OA_APPMODEL_OK;
    } catch (...) { return OA_APPMODEL_INVALID; }
}

extern "C" oa_appmodel_status oa_panel_appmodel_window_closed(oa_appmodel* model, uint64_t call) {
    namespace wire = abstraction::facade::applications;
    try {
        wire::HostActivationResult result;
        result.outcome = wire::HostActivationOutcome::Unavailable;
        result.reason = "Panel window closed";
        const auto record = wire::to_json(result);
        oa_appmodel_error error{};
        const auto status = oa_appmodel_complete_activation(model, call, {record.data(), record.size()}, &error);
        return status == OA_APPMODEL_OK ? status : oa_appmodel_callback_fault(model, call, &error);
    } catch (...) { oa_appmodel_error error{}; return oa_appmodel_callback_fault(model, call, &error); }
}
