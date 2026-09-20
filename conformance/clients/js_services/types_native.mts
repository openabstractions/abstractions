// Type-level consumer of the native connector's declarations, compiled with --run.
import type { Connector } from '@openabstractions/facade';
import { FrameError, NativeConnector, Status, runtimeEndpoint, selectRuntime, type ServerExpectation } from '@openabstractions/ipc';

export function native(): { connector: Connector; endpoint: string } {
  const connector = new NativeConnector();
  const status: Status = Status.Timeout;
  void new FrameError(status, 'message');
  return { connector, endpoint: runtimeEndpoint() };
}

export async function selected(): Promise<ServerExpectation> {
  const server = await selectRuntime({ timeout: 1000 });
  const kind: 1 | 2 = server.principalKind;
  void kind;
  return new NativeConnector().selectRuntime({ deadline: performance.now() + 1000 });
}
