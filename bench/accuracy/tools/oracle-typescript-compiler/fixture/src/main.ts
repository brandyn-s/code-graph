import { Formatter, normalize } from "./math.js";
export { clamp } from "./math.js";

export function render(raw: number): string {
  const formatter = new Formatter();
  return formatter.format(normalize(raw));
}
