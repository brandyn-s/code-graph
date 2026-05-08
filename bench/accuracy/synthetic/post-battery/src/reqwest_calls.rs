// Exercises reqwest HTTP client URL extraction.
//
// Every call here uses a literal string URL as the first argument —
// the form that httplink.go's URL extractor catches today (post #251).
// Phase C1 (D1) will extend this to non-literal URLs (consts,
// format!()) — that work belongs in a separate fixture so this one
// stays a pure regression-protector for the existing literal-URL path.

pub fn call_status() {
    let _ = reqwest::blocking::Client::new()
        .get("https://api.example.com/status")
        .send();
}

pub fn call_create() {
    let _ = reqwest::blocking::Client::new()
        .post("https://api.example.com/users")
        .send();
}

pub fn call_update() {
    let _ = reqwest::blocking::Client::new()
        .put("https://api.example.com/users/1")
        .send();
}

pub fn call_delete() {
    let _ = reqwest::blocking::Client::new()
        .delete("https://api.example.com/users/1")
        .send();
}
