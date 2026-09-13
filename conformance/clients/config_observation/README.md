# Generated C++ service binding

Run `python conformance/clients/config_observation/run.py --run --race --cmake PATH`
with the installed compiler/CMake toolchain. Help is the default.

The runner installs current config and independent facade-resolution packages into
a temporary prefix; unrelated capability headers are absent. An outside C++
consumer calls ConfigObserver, ConfigReader and ConfigEditor through the same
shared factory against an isolated Go runtime with selected temporary CAS storage.
It proves observed changes, cancellation, explicit deadline, reusable default
waiting, moved transport ownership and restart gap. It also binds an explicit
in-process generated test dispatcher using the same descriptor and rejects a
mismatched reference. No C++ service daemon or owner configuration is used.

Missing config/IPC dependencies must refuse by name. This fixture measures native
Windows/MSVC; a Darwin run remains subject to the current Program identity limit.
