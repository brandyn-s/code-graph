// Internal repo with method names that collide with Diesel's
// RunQueryDsl trait. Shadow target for the negative fixture.

pub struct AssetRepo;

impl AssetRepo {
    pub fn new() -> Self {
        AssetRepo
    }

    // Same name as Diesel's RunQueryDsl::get_result. Unrelated.
    pub fn get_result(&self) -> Result<i32, ()> {
        Ok(42)
    }

    // Same name as Diesel's RunQueryDsl::load. Unrelated.
    pub fn load(&self) -> Vec<i32> {
        vec![1, 2, 3]
    }

    // Same name as Diesel's RunQueryDsl::execute. Unrelated.
    pub fn execute(&self, _arg: i32) -> usize {
        7
    }
}
