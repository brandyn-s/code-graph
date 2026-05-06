// Canary fixture for the javascript tree-sitter grammar.
//
// Exercises features the code-graph JS extractor depends on:
//   - class + methods
//   - arrow function
//   - template literal
//   - destructuring
//   - async/await
//
// If the AST shape changes, extraction quality changes — drift_check fires.

class Service {
  constructor(name) {
    this.name = name;
  }

  async handle(target) {
    throw new Error(`service ${this.name}: ${target}`);
  }
}

const dispatchTo = async (h, name) => {
  await h.handle(name);
};

function parseInteger(s) {
  const n = parseInt(s, 10);
  return Number.isNaN(n) ? null : n;
}

const { handle } = new Service("test");

module.exports = { Service, dispatchTo, parseInteger, handle };
