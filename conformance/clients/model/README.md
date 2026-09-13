# Installed C++ model lookup

`abstraction::model_client` binds the generated ModelResolver interface to an
explicit shared-IPC endpoint. The aggregate facade exposes `Machine.ResolveModel`.
Lookup returns a typed optional download request. Submitting it is a separate
caller-owned job operation.

Install `abstraction-model/cpp` with CMake. Its installed package is
`abstraction_model`; the target is `abstraction::model_client`. Dependencies are
`abstraction_model_api`, `abstraction_download_request` and `abstraction_ipc`.
All are ordinary installed exports. `ABSTRACTION_MODEL_BUILD_CLIENT=OFF` builds
only the generated model API. Jobs-only facade builds retain their small closure.

For the integration fixture, install the aggregate facade SDK in a temporary
prefix, then run `run.py --help` followed by:

```sh
python conformance/clients/model/run.py --run --prefix "$PREFIX"
```

The fixture builds outside installed consumers. The direct executable links only
the model client; the aggregate executable resolves the model service, obtains a
portable request, submits it to the job service and reads the exact result bytes.
Both use a temporary Go runtime and an explicitly configured in-memory registry
pointing at a localhost HTTP fixture. Private destination mappings are refused.
No owner's model registry or installed runtime is used.

To prove the minimal distribution independently, install the model CMake project
into a separate prefix and configure this consumer with `MODEL_DIRECT_ONLY=ON`
and that prefix. Pass the resulting executable through `run.py --direct-probe`.
`--race` enables the Go race detector. This instrument currently refuses Darwin
as a conformance claim because shared Program proof remains unavailable there.

The additional model_generic consumer links only model plus facade_resolution and uses the generated ModelResolver descriptor. It calls the actual runtime, checks portable request codecs and private-mapping refusal. Model consumer builds are temporary to prevent stale package DIR cache reuse.
