// Canary fixture for the go tree-sitter grammar.
//
// Exercises features the code-graph Go extractor depends on:
//   - top-level + method functions
//   - interface dispatch
//   - struct literal
//   - type assertion
//   - goroutine + channel
//
// If the AST shape changes, extraction quality changes — drift_check fires.

package canary

import "fmt"

type Handler interface {
	Handle(name string) error
}

type Service struct {
	Name string
}

func (s *Service) Handle(name string) error {
	return fmt.Errorf("service %s: %s", s.Name, name)
}

func DispatchTo(h Handler, name string) error {
	return h.Handle(name)
}

func WithGoroutine(items []string) <-chan string {
	out := make(chan string, len(items))
	go func() {
		defer close(out)
		for _, x := range items {
			out <- x
		}
	}()
	return out
}

func TypeAssert(v any) (string, bool) {
	s, ok := v.(string)
	return s, ok
}
