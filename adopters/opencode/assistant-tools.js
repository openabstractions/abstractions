/** Adapt the emitted assistant catalogue's JSON Schema to OpenCode's host schema. */
export function assistantToolArguments(document, z) {
  let nodes = 0;
  function resolve(schema, active = new Set(), depth = 0) {
    if (++nodes > 4096 || depth > 32) throw new Error('Unsupported assistant schema');
    if (schema === true) return z.unknown();
    if (!schema || typeof schema !== 'object' || Array.isArray(schema)) throw new Error('Unsupported assistant schema');
    if (Object.keys(schema).every(key => ['description', 'title', '$comment'].includes(key))) return z.unknown();
    if (schema.$ref) {
      if (typeof schema.$ref !== 'string' || !schema.$ref.startsWith('#/') || active.has(schema.$ref)) throw new Error('Unsupported assistant schema reference');
      let target = document;
      for (const part of schema.$ref.slice(2).split('/')) {
        const key = part.replace(/~1/g, '/').replace(/~0/g, '~');
        if (!target || !Object.hasOwn(target, key)) throw new Error('Missing assistant schema reference');
        target = target[key];
      }
      return resolve(target, new Set([...active, schema.$ref]), depth + 1);
    }
    let value;
    if (Array.isArray(schema.type)) {
      if (schema.type.length < 2 || schema.type.length > 8 || new Set(schema.type).size !== schema.type.length) throw new Error('Unsupported assistant type union');
      value = z.union(schema.type.map(type => resolve({...schema, type}, active, depth + 1)));
    } else if (schema.enum) {
      if (!Array.isArray(schema.enum) || !schema.enum.length || schema.enum.some(item => item!==null&&!['string','boolean','number'].includes(typeof item))) throw new Error('Unsupported assistant enum');
      if (schema.enum.every(item=>typeof item==='string')) value = z.enum(schema.enum);
      else {
        const literals=schema.enum.map(item=>z.literal(item));
        value=literals.length===1?literals[0]:z.union(literals);
      }
    } else if (schema.type === 'string') {
      value = z.string();
      if (schema.minLength !== undefined) value = value.min(schema.minLength);
      if (schema.maxLength !== undefined) value = value.max(schema.maxLength);
      if (schema.pattern !== undefined) value = value.regex(new RegExp(schema.pattern));
    } else if (schema.type === 'integer' || schema.type === 'number') {
      value = z.number();
      if (schema.type === 'integer') value = value.int();
      if (schema.minimum !== undefined) value = value.min(schema.minimum);
      if (schema.maximum !== undefined) value = value.max(schema.maximum);
    } else if (schema.type === 'boolean') value = z.boolean();
    else if (schema.type === 'object') {
      value = z.object(properties(schema, active, depth));
      if (schema.additionalProperties === false) value = value.strict();
      else if (schema.additionalProperties && typeof schema.additionalProperties === 'object') value = value.catchall(resolve(schema.additionalProperties, active, depth + 1));
      else value = value.passthrough();
    } else if (schema.type === 'array') {
      value = z.array(resolve(schema.items, active, depth + 1));
      if (schema.minItems !== undefined) value = value.min(schema.minItems);
      if (schema.maxItems !== undefined) value = value.max(schema.maxItems);
    } else if (schema.anyOf) value = z.union(schema.anyOf.map(item => resolve(item, active, depth + 1)));
    else if (schema.type === 'null') value = z.null();
    else throw new Error('Unsupported assistant schema type');
    return schema.description ? value.describe(schema.description) : value;
  }
  function properties(schema, active = new Set(), depth = 0) {
    const required = new Set(schema.required ?? []);
    return Object.fromEntries(Object.entries(schema.properties ?? {}).map(([name, item]) => {
      const value = resolve(item, active, depth + 1);
      return [name, required.has(name) ? value : value.optional()];
    }));
  }
  if (document.type !== 'object') throw new Error('Assistant tool arguments must be an object');
  return properties(document);
}
