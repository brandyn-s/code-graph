// Post-battery synthetic fixture entry point.
//
// Exercises every extractor pattern that the 12-item PSM battery
// regression-protected on this code-graph build. Indexed via
// bench/accuracy/check_post_battery.py.

mod axum_routes;
mod impl_trait;
mod reqwest_calls;
mod safety_block;

fn main() {
    reqwest_calls::call_status();
    reqwest_calls::call_create();
    reqwest_calls::call_update();
    reqwest_calls::call_delete();

    let _ = axum_routes::build_router();

    let cat = impl_trait::Cat;
    impl_trait::Animal::speak(&cat);

    safety_block::do_unsafe_thing();
}
