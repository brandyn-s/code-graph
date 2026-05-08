// Pattern 3: URL computed via Rust's format!() macro from a base
// substitution and a static path. Pre-C1 this would slip past pathRe
// because the format string starts with `{`, not `/`. C1's
// formatMacroRe extracts the static path from any /path-shaped
// substring in the format string.

pub fn call_format() {
    let base = "https://api.example.com";
    let url = format!("{}/api/items", base);
    let _ = reqwest::blocking::Client::new()
        .get(&url)
        .send();
}
