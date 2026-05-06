// Canary fixture for the typescript tree-sitter grammar.
//
// Exercises features the code-graph TS extractor depends on:
//   - interface declaration
//   - class with methods
//   - generic function
//   - arrow function + async/await
//   - default + named exports
//
// If the AST shape changes, extraction quality changes — drift_check fires.

export interface Handler {
  handle(name: string): Promise<void>;
}

export class Service implements Handler {
  constructor(public readonly name: string) {}

  async handle(name: string): Promise<void> {
    throw new Error(`service ${this.name}: ${name}`);
  }
}

export const dispatchTo = async <H extends Handler>(
  h: H,
  name: string
): Promise<void> => {
  await h.handle(name);
};

export function parseInteger(s: string): number | null {
  const n = parseInt(s, 10);
  return Number.isNaN(n) ? null : n;
}

export default dispatchTo;
