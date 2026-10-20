# Reviewed Windows watcher input

The installer carries these existing reviewed bytes without rebuilding or
signing them. `build_installer.py` checks their exact size, SHA-256 and relative
CodeView path before preparing a payload.

- `watcher.node`: 528,896 bytes, SHA-256
  `0251ee3ba7027af84a0cb8e0c2ffa1c5989eacf21bc091f95a7c5c00cc486e12`.
- `LICENSE`: upstream MIT text, 1,077 bytes, SHA-256
  `5cfba9d5711264d45a2431a53c79f597cef12720cc7c764db2602daa92d0bd2a`.
- `source.tar`: 6,389,760 bytes, SHA-256
  `3256836353b9e26fcd225537a377885d6356552f170d4f58be955f9b17d364a8`.

The source archive was produced by Git from the retained exact OA build commit:

```text
git -c core.autocrlf=false archive --format=tar --prefix=watcher-052dc8d/ 052dc8d70871bc73d8ea1a30675f23347d144d98
```

Its parent source is upstream `parcel-bundler/watcher` tag `v2.5.1`, commit
`664695dbb2488d53f782c3e2b8791c4c114731fb`, with PR #277 commit
`d09df163078cd54e422c8fbacacbf0359be3f52e` cherry-picked onto it. The patched
Windows source has Git blob `e5741049edb336552c58acf0b4ad1e6ea8fe9248`.

`BUILD.md` is the retained 2026-11-10 review, with the original build inputs,
reproducibility evidence, license details and known limits. The recorded build
uses node-gyp 12.4.0, Node 24.19.0 headers, node-addon-api 7.0.0 with its locked
integrity, and MSVC 19.51.36256. In that build environment, set `CL=/Brepro` and
`_LINK_=/Brepro /PDBALTPATH:watcher.pdb`, then run `node-gyp rebuild --release`.
The retained module was independently reproduced in two folders with that
toolchain. This source export adds no new native build or signing claim.
