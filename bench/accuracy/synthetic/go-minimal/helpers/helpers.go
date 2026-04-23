package helpers

func Greet(name string) {
	innerGreet(name) // CALLS: helpers.Greet -> helpers.innerGreet
}

func innerGreet(_ string) {}
