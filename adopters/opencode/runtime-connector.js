/** The adopter's existing provider choice, shared by its service consumers.
 * An explicit connector keeps its caller-supplied transport and trust semantics.
 * Native loading remains lazy inside the existing IPC connectors.
 */
export async function runtimeConnector(injected) {
  if (injected !== undefined && injected !== null) return injected;
  if (typeof globalThis.Bun !== 'undefined') {
    const {BunNativeConnector} = await import('@openabstractions/ipc/bun');
    return new BunNativeConnector();
  }
  const {NativeConnector} = await import('@openabstractions/ipc');
  return new NativeConnector();
}
