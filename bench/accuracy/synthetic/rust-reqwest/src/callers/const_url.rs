// Pattern 2: URL bound to a top-level const, then referenced by name
// inside the function body. Pre-C1 this would emit no HTTP_CALLS edge
// because the function-line slice misses the `const` line. C1 walks
// the file for `const NAME: &str = "URL"` definitions and folds
// referenced URLs back into the function source for extraction.

const USERS_URL: &str = "https://api.example.com/api/users";

pub fn call_const() {
    let _ = reqwest::blocking::Client::new()
        .get(USERS_URL)
        .send();
}
