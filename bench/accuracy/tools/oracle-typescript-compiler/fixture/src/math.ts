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

interface Callback {
  (value: number): number;
}

export function wrap(callback: Callback): number {
  const local = () => normalize(callback(1));
  return local();
}
