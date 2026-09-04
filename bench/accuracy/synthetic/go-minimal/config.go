package main

// Config exercises struct field extraction (upstream 47116b8e / cb7cb444):
// fields live under struct_type -> field_declaration_list and must become
// nodes; the blank identifier is padding and must not.
type Config struct {
	Name    string
	Retries int
	_       [4]byte
	inner   *Config
}
