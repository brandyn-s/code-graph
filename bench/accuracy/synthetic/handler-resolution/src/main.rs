mod client;
mod decoy;
mod server;

fn main() {
    client::call_users();
    let _ = server::routes::build_router();
    let _ = decoy::list_users();
}
