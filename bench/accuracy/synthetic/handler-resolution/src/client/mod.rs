pub fn call_users() {
    let _ = reqwest::blocking::Client::new()
        .get("https://api.example.com/api/users")
        .send();
}
