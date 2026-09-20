// Type-level consumer of a package subpath export. node10 resolution cannot see
// subpath exports, so this file is compiled under nodenext only.
import { Machine, type Connector } from '@openabstractions/facade';
import { ResolverClient, type ResolveRequest, type ServiceReference } from '@openabstractions/facade/protocol';

export async function resolver(connector: Connector): Promise<ServiceReference | null> {
  const binding = await new Machine('endpoint', { connector }).resolverBinding();
  const request: ResolveRequest = { capability: 'abstraction.job', contracts: ['abstraction.job/acceptance@1'], guarantees: [], scope: 'any' };
  return (await new ResolverClient(binding).resolve(request)).reference;
}
