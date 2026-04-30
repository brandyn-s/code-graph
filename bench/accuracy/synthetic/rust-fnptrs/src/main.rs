// Synthetic fixture for testing the CALLS-to-Variable filter.
// Heavy use of fn pointers, closures, and trait object calls.

fn double(n: i32) -> i32 {
    n * 2
}

fn increment(n: i32) -> i32 {
    n + 1
}

fn apply(f: fn(i32) -> i32, n: i32) -> i32 {
    // This is a call through a fn pointer parameter — f is a Variable
    // in code-graph's model (function parameter).
    f(n)
}

fn pick(cond: bool) -> fn(i32) -> i32 {
    if cond {
        double
    } else {
        increment
    }
}

fn run() -> i32 {
    // 1. Direct fn call.
    let a = double(5);

    // 2. Call through fn-pointer variable.
    let f: fn(i32) -> i32 = double;
    let b = f(10);

    // 3. Call through pattern where fn is returned.
    let picker = pick(true);
    let c = picker(7);

    // 4. Call through closure.
    let triple = |n: i32| n * 3;
    let d = triple(4);

    // 5. Call through trait object (dyn Fn).
    let boxed: Box<dyn Fn(i32) -> i32> = Box::new(|n| n + 100);
    let e = boxed(1);

    // 6. Call through fn-pointer passed to a helper.
    let g = apply(increment, 20);

    a + b + c + d + e + g
}

fn main() {
    let total = run();
    println!("{}", total);
}
