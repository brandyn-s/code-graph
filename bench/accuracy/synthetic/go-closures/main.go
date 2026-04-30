// Synthetic fixture for testing the CALLS-to-Variable filter introduced
// in pipeline.buildEdgesFromResults. Heavy closure use — every call here
// targets a closure stored in a Variable, not a Function/Method.
//
// Expected behavior BEFORE the filter landed:
//   code-graph emitted CALLS edges target_label=Variable.
//   Oracle emits the same (bare calls to variable names).
//   Match rate: probably decent.
//
// Expected behavior AFTER the filter (current state):
//   code-graph DROPS these CALLS because target is Variable.
//   Oracle still emits them.
//   Result: closure CALLS show up as oracle FNs.
//
// Red-team goal: does the filter drop legitimate closure CALLS that a
// user of the graph would actually want to see?
package main

import "fmt"

func main() {
	// 1. Direct closure call.
	add := func(a, b int) int { return a + b }
	result1 := add(1, 2)

	// 2. Closure passed as variable and called through it.
	var op func(int, int) int
	op = add
	result2 := op(3, 4)

	// 3. Function returned from a function, then called.
	makeAdder := func(base int) func(int) int {
		return func(n int) int { return base + n }
	}
	add5 := makeAdder(5)
	result3 := add5(10)

	// 4. Closure in a slice, iterated.
	ops := []func(int) int{
		func(n int) int { return n * 2 },
		func(n int) int { return n + 1 },
	}
	acc := 0
	for _, f := range ops {
		acc = f(acc)
	}

	// 5. Method value — binding a method to a variable.
	c := counter{v: 100}
	getter := c.get
	result4 := getter()

	fmt.Println(result1, result2, result3, acc, result4)
}

type counter struct {
	v int
}

func (c counter) get() int {
	return c.v
}
