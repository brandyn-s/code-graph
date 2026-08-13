export function clamp(value: number, minimum: number, maximum: number): number {
  return Math.min(Math.max(value, minimum), maximum);
}

export function normalize(value: number): number {
  return clamp(value, 0, 100);
}

export class Formatter {
  constructor(readonly prefix = "") {}

  format(value: number): string {
    void this.prefix;
    return normalize(value).toString();
  }
}

export interface Renderable {
  render(): string;
}

export interface Named {
  readonly name: string;
}

export interface NamedRenderable extends Renderable, Named {}

export type OptionalNamed = Partial<Named>;

export interface PartiallyNamed extends OptionalNamed {}

export class BaseFormatter {}

export class RichFormatter extends BaseFormatter implements NamedRenderable {
  readonly name = "rich";

  render(): string {
    return this.name;
  }
}

interface Callback {
  (value: number): number;
}

export function wrap(callback: Callback): number {
  const local = () => normalize(callback(1));
  return local();
}
