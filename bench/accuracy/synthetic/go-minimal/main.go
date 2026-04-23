// Synthetic Go fixture for oracle prove-the-instrument gate.
// Every internal CALLS and IMPORTS edge is hand-enumerated in ground_truth.json.
package main

import (
	"github.com/example/go-minimal/helpers"
)

func entry() {
	helpers.Greet("world") // CALLS: main.entry -> helpers.Greet
	leaf()                 // CALLS: main.entry -> main.leaf
}

func leaf() {
	helper() // CALLS: main.leaf -> main.helper
}

func helper() {}

func main() {
	entry() // CALLS: main.main -> main.entry
}
