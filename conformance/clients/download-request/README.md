# Installed download request codec

Run `python conformance/clients/download-request/run.py --run --cmake <cmake>`
from the source workspace or public charter checkout with sibling repositories.
Use `--generator` when the default compiler generator is unsuitable. No arguments
show help. Each install uses a fresh ignored prefix under `.build/req`.

The runner builds the existing download CMake package, installs its generated
request header, compares that installed header byte-for-byte with generated source,
and builds a copied outside consumer using `find_package(abstraction_download)`.
The executable emits three request documents. The generated Go reader checks
exact decoded fields and canonical byte equality, then rejects four invalid
payloads. Fixtures include omitted defaults, multiple sources, signed 64-bit
maximum, quote/backslash/newline/NUL and non-BMP Unicode.

This checks codec interoperability and existing package installation. The package
build includes legacy job/download libraries and shared IPC dependencies. No
standalone request-only CMake target is claimed. Codec-valid empty requests and
synthetic source schemes are test cases; execution-provider admission policy is
separate. No request is submitted, network accessed, provider run or product
installed on the machine. Native macOS/Linux execution is not recorded here.
