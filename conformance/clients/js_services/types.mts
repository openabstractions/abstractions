// Type-level consumer of the installed packages' declarations. It is compiled with
// tsc --noEmit --strict and never run: a declaration that disagrees with a
// generated name, field, vocabulary or client signature fails the compile.
import { Machine, ResolutionError, runtimeUnavailable, type Connector } from '@openabstractions/facade';
import * as asks from '@openabstractions/asks';
import * as config from '@openabstractions/config';
import * as request from '@openabstractions/download-request';
import * as jobs from '@openabstractions/job-acceptance';
import * as logging from '@openabstractions/logging';
import * as model from '@openabstractions/model';
import * as rights from '@openabstractions/rights';
import * as router from '@openabstractions/router';
import * as storage from '@openabstractions/storage-content';

type Expect<T extends true> = T;
type Equal<A, B> = (<X>() => X extends A ? 1 : 2) extends <X>() => X extends B ? 1 : 2 ? true : false;

export async function consumer(connector: Connector) {
  const machine = new Machine('endpoint', { connector, timeout: 1000 });
  const binding = await machine.resolveService('abstraction.job/acceptance@1', { maxFrame: 2_097_152 });
  const acceptance = binding.client(jobs.RecoverableAcceptanceClient);
  const operations = (await machine.resolveService('abstraction.job/operations@1')).client(jobs.OperationControlClient);

  const window = await acceptance.getHistoryWindow();
  const req: request.Request = { ...request.newRequest(), sources: [{ scheme: 'https', locator: 'https://example.com/m' }] };
  const submission: jobs.Submission = {
    ...jobs.newSubmission(),
    identity: { ...jobs.newRequestIdentity(), key: 'k', historyEpoch: window.historyEpoch },
    kind: 'download',
    spec: request.encode(req),
    requiredGuarantees: [...jobs.admissionGuarantees],
  };
  const accepted = await acceptance.submit(submission);
  const outcome: jobs.AcceptanceOutcome = accepted.outcome;
  if (outcome === jobs.AcceptanceOutcome.Accepted && accepted.receipt) {
    const receipt: jobs.Receipt = accepted.receipt;
    const observed = await operations.observeWork(receipt.identity);
    if (observed.snapshot?.state === jobs.WorkState.Complete) {
      const read = await operations.readResult(receipt.identity, 0n, 65_536n);
      const bytes: Uint8Array | undefined = read.chunk?.data;
      void bytes;
    }
    const cause: jobs.FailureCause | (string & {}) | undefined = observed.snapshot?.failure?.cause;
    void cause;
  }
  // @ts-expect-error a closed vocabulary refuses a word it does not have
  const wrong: jobs.AcceptanceOutcome = 'maybe';
  void wrong;
  // @ts-expect-error the pre-rename spelling is gone
  void acceptance.Submit;

  const decision = await binding.client(rights.AuthorizationClient).decide('action', 'resource');
  const revision: string = decision.policyRevision;
  void revision;
  const lookup = await binding.client(model.ModelResolverClient).resolve({ ...model.newRef(), repo: 'weights' });
  const imported: request.Request | null = lookup.request;
  void imported;
  await binding.client(logging.SinkClient).write({ ...logging.newRecord(), msg: 'typed' });
  void binding.client(config.ConfigReaderClient);
  void binding.client(asks.QuestionApplicationClient);
  void binding.client(router.RouterClient);
  void binding.client(storage.ContentReaderClient);
  return { accepted, window };
}

export type Checks = [
  Expect<Equal<jobs.WorkState, 'pending' | 'running' | 'transferred' | 'complete' | 'failed' | 'cancelled'>>,
  Expect<Equal<(typeof jobs.RecoverableAcceptanceService)['wireName'], 'abstraction.job/acceptance@1'>>,
  Expect<Equal<ReturnType<typeof request.decode>, request.Request>>,
];
export const errors = [ResolutionError, jobs.ServiceError, jobs.DispatchError, jobs.Refusal];

// Absence: an installed-runtime machine and the typed resolution error an adopter catches.
export async function absent(connector: Connector): Promise<string> {
  try {
    await new Machine(null, { connector }).resolveService('abstraction.logging/sink@1');
    return 'resolved';
  } catch (error) {
    if (!(error instanceof ResolutionError)) throw error;
    const status: string = error.status;
    const where: string | null = error.lookedFor;
    const contract: string | null = error.capability === null ? null : error.contract;
    const cause: unknown = error.cause;
    void cause;
    return status === runtimeUnavailable ? `${contract} at ${where}` : status;
  }
}
export type AbsenceChecks = [Expect<Equal<typeof runtimeUnavailable, 'runtime_unavailable'>>];
