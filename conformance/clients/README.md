# Service clients

These checks build the real Go host and an application client, run both against
isolated local endpoints, and inspect observable service behavior. They do not
install a service or exercise an upstream application integration.

Run a check with `python logging/run.py --help` (or the corresponding capability
directory). Checks name the platform they measure and refuse unsupported ones.
The C++ consumer builds through installed CMake packages.

In a public `abstractions` checkout, put the required `abstraction-*` repositories
beside it. The Go host uses those source checkouts through a temporary workspace,
and CMake resolves their sibling sources before testing a fresh install prefix.
In the private source tree the same checks use `openabstractions-flat/`.
Dependencies must already be cached; set `GOPROXY=https://proxy.golang.org,direct`
to allow Go to download missing dependencies. No repositories are fetched by a
check. Build outputs and temporary service state stay under `.build/`.

Maintainer manifests bind results to source and toolchain inputs in the private
tree. Recorded results describe their stated scenario; they do not establish
whole-provider conformance or installed-service crash recovery.
