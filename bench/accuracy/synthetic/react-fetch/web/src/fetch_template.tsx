// Pattern 2: fetch with a backtick template literal carrying a
// static-prefix URL. Pre-C2 this slipped past pathRe because the
// quote characters are backticks, not " or '. C2's templateLiteralRe
// captures the backtick contents and pathInFormatRe extracts the
// /path-shaped substring.

export async function fetchOrders(baseUrl: string) {
  const r = await fetch(`${baseUrl}/api/orders`);
  return r.json();
}
