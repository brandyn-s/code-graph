// Entry point: exercises every reqwest-URL shape so the indexer sees
// a single binary call site for each.

mod callers;
mod server;

fn main() {
    callers::literal::call_literal();
    callers::const_url::call_const();
    callers::format_url::call_format();

    let _ = server::routes::build_router();
}
