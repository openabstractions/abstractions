// This tool manages policies belonging to OpenCode's own runtime identity.
// The resolver is supplied by the plugin so no account or program is asserted here.
const policyName = /^[a-z0-9][a-z0-9_-]{0,63}$/;
const credentialName = /^[A-Za-z0-9_.-]{1,64}$/;
const providerName = /^[a-z0-9_-]{1,64}$/;
const safeRevision = (value) => typeof value === 'string' && value.length <= 128 && !/[\x00-\x1f\x7f]/.test(value);
const readOutcomes = new Set(['found', 'unknown', 'invalid', 'forbidden', 'unavailable']);
const editOutcomes = new Set(['applied', 'conflict', 'unknown', 'invalid', 'no_secure_store', 'forbidden', 'unavailable']);

function candidateList(candidates) {
  if (!Array.isArray(candidates) || candidates.length < 1 || candidates.length > 8) {
    throw new Error('Provide 1 to 8 ordered candidates.');
  }
  const seen = new Set();
  return candidates.map((candidate) => {
    const {model, placement} = candidate ?? {};
    const credential = candidate?.credentialName ?? '';
    const provider = candidate?.provider ?? '';
    if (typeof model !== 'string' || !model || model.length > 256 || model.trim() !== model) {
      throw new Error('Each candidate needs a model name.');
    }
    if (placement !== 'local' && placement !== 'hosted') {
      throw new Error('Each candidate needs local or hosted placement.');
    }
    if (placement === 'local' && credential !== '') {
      throw new Error('A local candidate cannot use a credential.');
    }
    if (placement === 'hosted' && !credentialName.test(credential)) {
      throw new Error('A hosted candidate needs a credential name, never a key.');
    }
    if (provider !== '' && (typeof provider !== 'string' || !providerName.test(provider))) {
      throw new Error('Provider pin must name one logical OA declaration, never an endpoint.');
    }
    const key = JSON.stringify([model, placement, credential, provider]);
    if (seen.has(key)) throw new Error('Duplicate candidate.');
    seen.add(key);
    return {model, placement, credential, ...(provider ? {provider} : {})};
  });
}

function requireName(name) {
  if (!policyName.test(name ?? '')) throw new Error('Invalid policy name.');
}

function requireRevision(value) {
  if (!safeRevision(value)) {
    throw new Error('Expected revision must be an opaque stored revision, or empty to create a policy.');
  }
}

function result(change, action, name) {
  if (!change || !editOutcomes.has(change.outcome) || !safeRevision(change.revision)) {
    throw new Error('Invalid policy change reply.');
  }
  return JSON.stringify({kind: 'stored_policy', action, name, outcome: change.outcome, revision: change.revision});
}

/** Build the OpenCode custom tool. resolveOperator must bind the current OpenCode caller. */
export function createSelectionPolicyTool({tool, resolveOperator}) {
  if (typeof tool !== 'function' || typeof resolveOperator !== 'function') {
    throw new TypeError('A tool helper and caller-bound operator resolver are required.');
  }
  const candidate = tool.schema.object({
    model: tool.schema.string(),
    placement: tool.schema.enum(['local', 'hosted']),
    credentialName: tool.schema.string().optional(),
    provider: tool.schema.string().optional(),
  });
  return tool({
    description: 'Read or manage an OpenAbstractions model preference owned by this OpenCode instance. Only put or delete after the user explicitly asks. A candidate may pin a logical OA provider declaration. Hosted credentials are names, never keys. A policy grants no execution rights.',
    args: {
      action: tool.schema.enum(['read', 'put', 'delete']),
      name: tool.schema.string(),
      expectedRevision: tool.schema.string().optional(),
      candidates: tool.schema.array(candidate).optional(),
    },
    async execute(args, context) {
      requireName(args.name);
      if (args.action === 'read') {
        if (args.expectedRevision !== undefined || args.candidates !== undefined) {
          throw new Error('Read accepts only a policy name.');
        }
        const read = await (await resolveOperator()).read('', args.name);
        if (!read || !readOutcomes.has(read.outcome) || !safeRevision(read.revision)) {
          throw new Error('Invalid policy read reply.');
        }
        if (read.outcome === 'found') {
          if (read.policy?.name !== args.name || !Array.isArray(read.policy.candidates)) {
            throw new Error('Invalid stored policy reply.');
          }
          return JSON.stringify({kind: 'stored_policy', action: 'read', name: args.name,
            outcome: read.outcome, revision: read.revision, candidates: read.policy.candidates});
        }
        return JSON.stringify({kind: 'stored_policy', action: 'read', name: args.name,
          outcome: read.outcome, revision: read.revision});
      }

      requireRevision(args.expectedRevision);
      const candidates = args.action === 'put' ? candidateList(args.candidates) : undefined;
      if (args.action === 'delete' && args.candidates !== undefined) {
        throw new Error('Delete accepts no candidates.');
      }
      if (args.action !== 'put' && args.action !== 'delete') throw new Error('Invalid policy action.');
      if (typeof context?.ask !== 'function') throw new Error('OpenCode cannot ask for policy-edit permission.');
      const summary = args.action === 'put'
        ? candidates.map((candidate, index) => `${index + 1}. ${JSON.stringify(candidate.model)} ${candidate.placement}${candidate.credential ? ` credential ${JSON.stringify(candidate.credential)}` : ''}${candidate.provider ? ` provider ${JSON.stringify(candidate.provider)}` : ''}`).join('; ')
        : 'delete';
      await context.ask({
        permission: 'openabstractions_selection_policy',
        patterns: [`${args.action} policy ${args.name}: ${summary}`],
        always: [],
        metadata: {action: args.action, name: args.name, expectedRevision: args.expectedRevision,
          ...(candidates ? {candidates} : {})},
      });
      const operator = await resolveOperator();
      const change = args.action === 'put'
        ? await operator.put('', args.expectedRevision, {name: args.name, candidates})
        : await operator.deletePolicy('', args.expectedRevision, args.name);
      return result(change, args.action, args.name);
    },
  });
}
