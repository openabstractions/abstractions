// Fixture host expectations shared by installed C++ conformance consumers.
//
// A proof that installs no runtime names its fixture host program. The consumer
// then requires this user's principal and that program of every resolved
// service. The principal takes the form the platform's Program proof reports:
// the account SID on Windows and the numeric POSIX uid elsewhere.
#pragma once

#include <abstraction/ipc/client.hpp>
#include <abstraction/ipc/process.hpp>

#include <string>
#ifndef _WIN32
#include <unistd.h>
#endif

namespace oa_fixture {

inline abstraction::ipc::ServerExpectation host(const char* program) {
#ifdef _WIN32
    return {OA_IPC_PRINCIPAL_WINDOWS_SID, abstraction::ipc::process_user_sid(), program};
#else
    return {OA_IPC_PRINCIPAL_POSIX_UID, std::to_string(getuid()), program};
#endif
}

}  // namespace oa_fixture
