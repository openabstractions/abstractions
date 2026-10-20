# Changes

## 0.8.0

### Lemonade: OA-native build

- OA Lemonade refuses to start without its OA runtime.
- The off switch is removed, and every startup refusal is typed.
- Lemonade's own downloads are refused while OA downloads are enabled.
- Startup selects an independently trusted runtime.
- The MSI no longer adds Start menu or desktop shortcuts.
- The native build keeps long backend paths working.
- The native adapter uses the shared logging handler.
