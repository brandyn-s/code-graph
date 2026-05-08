// Exercises impl Trait for Struct extraction.
//
// The trait + struct are both LOCAL to this fixture — so the
// resolver can populate both QNs at extraction time. This is the
// happy-path baseline for IMPLEMENTS edges; PSM's traitQN-empty
// problem is in the cross-crate / external-trait case, which is
// covered by adversarial fixtures, not this regression-protector.

pub trait Animal {
    fn speak(&self) -> &'static str;
}

pub struct Cat;

impl Animal for Cat {
    fn speak(&self) -> &'static str {
        "meow"
    }
}

pub struct Dog;

impl Animal for Dog {
    fn speak(&self) -> &'static str {
        "woof"
    }
}
