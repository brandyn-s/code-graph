// Pattern 1: URL passed as a literal string directly to the reqwest
// chained method. The pre-existing extractor (urlRe / pathRe in
// httplink.go) already catches this. C1 keeps it as a control case.

pub fn call_literal() {
    let _ = reqwest::blocking::Client::new()
        .get("https://api.example.com/api/orders")
        .send();
}
