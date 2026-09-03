package cbm

import (
	"slices"
	"testing"

	"github.com/brandyn-s/code-graph/internal/lang"
)

// =====================================================================
// Group A: OOP Languages
// =====================================================================

// --- Java ---
func TestJavaClass_Regression(t *testing.T) {
	src := []byte("public class Animal { private String name; public String getName() { return name; } }")
	r, err := ExtractFile(src, lang.Java, "t", "Animal.java")
	if err != nil {
		t.Fatal(err)
	}
	assertHasName(t, defsWithLabel(r, "Class"), "Animal")
}

func TestJavaMethod_Regression(t *testing.T) {
	src := []byte("public class Svc { public void doWork() {} public int compute(int x) { return x; } }")
	r, err := ExtractFile(src, lang.Java, "t", "Svc.java")
	if err != nil {
		t.Fatal(err)
	}
	assertHasName(t, defsWithLabel(r, "Method"), "doWork")
	assertHasName(t, defsWithLabel(r, "Method"), "compute")
}

func TestJavaInterface_Regression(t *testing.T) {
	src := []byte("public interface Repository { void save(Object o); Object findById(long id); }")
	r, err := ExtractFile(src, lang.Java, "t", "Repo.java")
	if err != nil {
		t.Fatal(err)
	}
	defs := r.Definitions
	found := false
	for _, d := range defs {
		if d.Name == "Repository" {
			found = true
		}
	}
	if !found {
		t.Error("interface Repository not found")
	}
}

// =====================================================================
// Group B: Systems Languages
// =====================================================================

// --- Rust ---
func TestRustFunction_Regression(t *testing.T) {
	src := []byte("fn main() { println!(\"Hello\"); }\npub fn add(a: i32, b: i32) -> i32 { a + b }\n")
	r, err := ExtractFile(src, lang.Rust, "t", "main.rs")
	if err != nil {
		t.Fatal(err)
	}
	assertHasName(t, defsWithLabel(r, "Function"), "main")
	assertHasName(t, defsWithLabel(r, "Function"), "add")
}

func TestRustStruct_Regression(t *testing.T) {
	src := []byte("pub struct Point { pub x: f64, pub y: f64 }\nimpl Point { pub fn new(x: f64, y: f64) -> Self { Point { x, y } } }\n")
	r, err := ExtractFile(src, lang.Rust, "t", "point.rs")
	if err != nil {
		t.Fatal(err)
	}
	assertHasName(t, defsWithLabel(r, "Class"), "Point")
	assertHasName(t, defsWithLabel(r, "Method"), "new")
}

func TestRustEnum_Regression(t *testing.T) {
	src := []byte("pub enum Direction { North, South, East, West }\n")
	r, err := ExtractFile(src, lang.Rust, "t", "dir.rs")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Definitions) < 1 {
		t.Error("expected >=1 definition for Rust enum")
	}
}

// --- Go ---
func TestGoFunction_Regression(t *testing.T) {
	src := []byte("package main\nfunc Greet(name string) string { return \"Hello, \" + name }\nfunc main() { Greet(\"World\") }\n")
	r, err := ExtractFile(src, lang.Go, "t", "main.go")
	if err != nil {
		t.Fatal(err)
	}
	assertHasName(t, defsWithLabel(r, "Function"), "Greet")
	assertHasName(t, defsWithLabel(r, "Function"), "main")
}

func TestGoStruct_Regression(t *testing.T) {
	src := []byte("package main\ntype Server struct { Host string; Port int }\nfunc (s *Server) Start() error { return nil }\n")
	r, err := ExtractFile(src, lang.Go, "t", "server.go")
	if err != nil {
		t.Fatal(err)
	}
	assertHasName(t, defsWithLabel(r, "Class"), "Server")
	assertHasName(t, defsWithLabel(r, "Method"), "Start")
}

func TestGoInterface_Regression(t *testing.T) {
	src := []byte("package main\ntype Handler interface { ServeHTTP() error; Close() }\n")
	r, err := ExtractFile(src, lang.Go, "t", "handler.go")
	if err != nil {
		t.Fatal(err)
	}
	defs := r.Definitions
	found := false
	for _, d := range defs {
		if d.Name == "Handler" {
			found = true
		}
	}
	if !found {
		t.Error("interface Handler not found")
	}
}

// --- C ---
func TestCFunction_Regression(t *testing.T) {
	src := []byte("#include <stdio.h>\nint add(int a, int b) { return a + b; }\nint main() { printf(\"%d\\n\", add(1,2)); return 0; }\n")
	r, err := ExtractFile(src, lang.C, "t", "main.c")
	if err != nil {
		t.Fatal(err)
	}
	assertHasName(t, defsWithLabel(r, "Function"), "add")
	assertHasName(t, defsWithLabel(r, "Function"), "main")
}

func TestCStruct_Regression(t *testing.T) {
	src := []byte("struct Point { int x; int y; };\nvoid print_point(struct Point p) { /* ... */ }\n")
	r, err := ExtractFile(src, lang.C, "t", "point.c")
	if err != nil {
		t.Fatal(err)
	}
	assertHasName(t, defsWithLabel(r, "Function"), "print_point")
}

// --- C++ ---
func TestCppFunction_Regression(t *testing.T) {
	src := []byte("#include <string>\nstd::string greet(const std::string& name) { return \"Hello \" + name; }\nint main() { return 0; }\n")
	r, err := ExtractFile(src, lang.CPP, "t", "main.cpp")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Definitions) < 1 {
		t.Error("expected >=1 definition from C++ file")
	}
}

func TestCppClass_Regression(t *testing.T) {
	src := []byte("class Animal {\npublic:\n    std::string name;\n    Animal(std::string n) : name(n) {}\n    void speak() { /* ... */ }\n};\n")
	r, err := ExtractFile(src, lang.CPP, "t", "Animal.cpp")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Definitions) < 1 {
		t.Error("expected >=1 definition from C++ class")
	}
}

// --- CUDA ---
func TestCUDAKernel_Regression(t *testing.T) {
	src := []byte("__global__ void vectorAdd(float *a, float *b, float *c, int n) {\n    int i = blockIdx.x * blockDim.x + threadIdx.x;\n    if (i < n) c[i] = a[i] + b[i];\n}\nint main() { return 0; }\n")
	r, err := ExtractFile(src, lang.CUDA, "t", "vector.cu")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Definitions) < 1 {
		t.Error("expected >=1 definition from CUDA file")
	}
}

// =====================================================================
// Group C: Dynamic Languages
// =====================================================================

// --- Python ---
func TestPythonFunction_Regression(t *testing.T) {
	src := []byte("def greet(name: str) -> str:\n    return f'Hello {name}'\n\ndef main():\n    print(greet('World'))\n")
	r, err := ExtractFile(src, lang.Python, "t", "hello.py")
	if err != nil {
		t.Fatal(err)
	}
	assertHasName(t, defsWithLabel(r, "Function"), "greet")
	assertHasName(t, defsWithLabel(r, "Function"), "main")
}

func TestPythonClass_Regression(t *testing.T) {
	src := []byte("class Animal:\n    def __init__(self, name: str):\n        self.name = name\n    def speak(self) -> str:\n        return f'I am {self.name}'\n")
	r, err := ExtractFile(src, lang.Python, "t", "animal.py")
	if err != nil {
		t.Fatal(err)
	}
	assertHasName(t, defsWithLabel(r, "Class"), "Animal")
	assertHasName(t, defsWithLabel(r, "Method"), "speak")
}

func TestPythonDecorator_Regression(t *testing.T) {
	src := []byte("class Router:\n    @staticmethod\n    def route(path: str):\n        def decorator(func): return func\n        return decorator\n")
	r, err := ExtractFile(src, lang.Python, "t", "router.py")
	if err != nil {
		t.Fatal(err)
	}
	assertHasName(t, defsWithLabel(r, "Class"), "Router")
}

// --- JavaScript ---
func TestJavaScriptFunction_Regression(t *testing.T) {
	src := []byte("function greet(name) { return 'Hello ' + name; }\nconst add = (a, b) => a + b;\nmodule.exports = { greet, add };\n")
	r, err := ExtractFile(src, lang.JavaScript, "t", "utils.js")
	if err != nil {
		t.Fatal(err)
	}
	assertHasName(t, defsWithLabel(r, "Function"), "greet")
}

func TestJavaScriptClass_Regression(t *testing.T) {
	src := []byte("class Animal {\n    constructor(name) { this.name = name; }\n    speak() { return `I am ${this.name}`; }\n}\nmodule.exports = Animal;\n")
	r, err := ExtractFile(src, lang.JavaScript, "t", "Animal.js")
	if err != nil {
		t.Fatal(err)
	}
	assertHasName(t, defsWithLabel(r, "Class"), "Animal")
	assertHasName(t, defsWithLabel(r, "Method"), "speak")
}

// --- TypeScript ---
func TestTypeScriptFunction_Regression(t *testing.T) {
	src := []byte("export function greet(name: string): string { return `Hello ${name}`; }\nexport const add = (a: number, b: number): number => a + b;\n")
	r, err := ExtractFile(src, lang.TypeScript, "t", "utils.ts")
	if err != nil {
		t.Fatal(err)
	}
	assertHasName(t, defsWithLabel(r, "Function"), "greet")
}

func TestTypeScriptInterface_Regression(t *testing.T) {
	src := []byte("export interface Repository<T> { findById(id: number): T; save(entity: T): void; delete(id: number): void; }\n")
	r, err := ExtractFile(src, lang.TypeScript, "t", "repo.ts")
	if err != nil {
		t.Fatal(err)
	}
	assertHasName(t, defsWithLabel(r, "Interface"), "Repository")
	methods := defsWithLabel(r, "Method")
	assertHasName(t, methods, "findById")
	assertHasName(t, methods, "save")
	assertHasName(t, methods, "delete")
}

func TestTypeScriptClass_Regression(t *testing.T) {
	src := []byte("export class UserService {\n    private users: Map<number, string> = new Map();\n    add(id: number, name: string): void { this.users.set(id, name); }\n    get(id: number): string | undefined { return this.users.get(id); }\n}\n")
	r, err := ExtractFile(src, lang.TypeScript, "t", "UserService.ts")
	if err != nil {
		t.Fatal(err)
	}
	assertHasName(t, defsWithLabel(r, "Class"), "UserService")
}

func TestTypeScriptHeritageClauseKinds_Regression(t *testing.T) {
	src := []byte(`
interface Renderable {}
interface Named {}
interface NamedRenderable<T> extends Renderable<T>, Named {}
class BaseFormatter {}
class RichFormatter extends BaseFormatter<string> implements NamedRenderable<string>, Renderable<string> {}
`)
	r, err := ExtractFile(src, lang.TypeScript, "t", "format.ts")
	if err != nil {
		t.Fatal(err)
	}
	definitions := make(map[string]Definition)
	for _, definition := range r.Definitions {
		definitions[definition.Name] = definition
	}
	if got := definitions["NamedRenderable"].ExtendsTypes; !slices.Equal(got, []string{"Renderable", "Named"}) {
		t.Fatalf("NamedRenderable extends = %v, want [Renderable Named]", got)
	}
	rich := definitions["RichFormatter"]
	if !slices.Equal(rich.ExtendsTypes, []string{"BaseFormatter"}) {
		t.Fatalf("RichFormatter extends = %v, want [BaseFormatter]", rich.ExtendsTypes)
	}
	if !slices.Equal(rich.ImplementsTypes, []string{"NamedRenderable", "Renderable"}) {
		t.Fatalf("RichFormatter implements = %v, want [NamedRenderable Renderable]", rich.ImplementsTypes)
	}
}

// --- TSX ---
func TestTSXComponent_Regression(t *testing.T) {
	src := []byte("import React from 'react';\ninterface Props { name: string; }\nexport function Greeting({ name }: Props) {\n    return <div>Hello {name}</div>;\n}\nexport default Greeting;\n")
	r, err := ExtractFile(src, lang.TSX, "t", "Greeting.tsx")
	if err != nil {
		t.Fatal(err)
	}
	assertHasName(t, defsWithLabel(r, "Function"), "Greeting")
}

// --- Bash ---
func TestBashFunction_Regression(t *testing.T) {
	src := []byte("#!/usr/bin/env bash\ngreet() {\n    echo \"Hello $1\"\n}\nbuild_project() {\n    go build ./...\n}\ngreet World\n")
	r, err := ExtractFile(src, lang.Bash, "t", "build.sh")
	if err != nil {
		t.Fatal(err)
	}
	assertHasName(t, defsWithLabel(r, "Function"), "greet")
	assertHasName(t, defsWithLabel(r, "Function"), "build_project")
}

// --- Nix ---
func TestNixFunction_Regression(t *testing.T) {
	src := []byte("{ pkgs ? import <nixpkgs> {} }:\nlet\n  greet = name: \"Hello ${name}\";\n  build = src: pkgs.stdenv.mkDerivation { inherit src; };\nin\n{ inherit greet build; }\n")
	r, err := ExtractFile(src, lang.Nix, "t", "default.nix")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Definitions) < 1 {
		t.Error("expected >=1 definition from Nix file")
	}
}

// =====================================================================
// Group D: Functional Languages
// =====================================================================

// =====================================================================
// Group E: Config / Markup Languages
// =====================================================================

// --- YAML ---
func TestYAMLMapping_Regression(t *testing.T) {
	src := []byte("server:\n  host: localhost\n  port: 8080\ndatabase:\n  url: postgres://localhost/mydb\n")
	r, err := ExtractFile(src, lang.YAML, "t", "config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	vars := defsWithLabel(r, "Variable")
	if len(vars) < 1 {
		t.Errorf("expected >=1 Variable from YAML, got 0")
	}
}

// --- Dockerfile ---
func TestDockerfileInstruction_Regression(t *testing.T) {
	src := []byte("FROM golang:1.21 AS builder\nWORKDIR /app\nCOPY . .\nRUN go build -o server ./cmd/server\nFROM alpine:3.18\nCOPY --from=builder /app/server /usr/local/bin/server\nEXPOSE 8080\nCMD [\"server\"]\n")
	r, err := ExtractFile(src, lang.Dockerfile, "t", "Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Definitions) < 1 {
		t.Error("expected >=1 definition from Dockerfile")
	}
}

// --- HTML ---
func TestHTMLElements_Regression(t *testing.T) {
	src := []byte("<!DOCTYPE html><html><head><title>Test</title></head><body><h1>Hello</h1><p>World</p></body></html>")
	r, err := ExtractFile(src, lang.HTML, "t", "index.html")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Definitions) < 1 {
		t.Error("expected >=1 definition from HTML")
	}
}

// --- SQL ---
func TestSQLTable_Regression(t *testing.T) {
	src := []byte("CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT NOT NULL, email TEXT UNIQUE);\nCREATE INDEX idx_users_email ON users(email);\n")
	r, err := ExtractFile(src, lang.SQL, "t", "schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Definitions) < 1 {
		t.Error("expected >=1 definition from SQL")
	}
}

func TestSQLFunction_Regression(t *testing.T) {
	src := []byte("CREATE FUNCTION get_user_count() RETURNS INTEGER AS $$ SELECT COUNT(*) FROM users; $$ LANGUAGE SQL;\n")
	r, err := ExtractFile(src, lang.SQL, "t", "funcs.sql")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Definitions) < 1 {
		t.Error("expected >=1 definition from SQL function")
	}
}

// --- CSS ---
func TestCSSRules_Regression(t *testing.T) {
	src := []byte(".container { display: flex; width: 100%; }\n.button { background: #007bff; color: white; border: none; }\n@media (max-width: 768px) { .container { flex-direction: column; } }\n")
	r, err := ExtractFile(src, lang.CSS, "t", "styles.css")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Definitions) < 1 {
		t.Error("expected >=1 definition from CSS")
	}
}

// --- HCL (Terraform) ---
func TestHCLResource_Regression(t *testing.T) {
	src := []byte("resource \"aws_instance\" \"web\" {\n  ami           = \"ami-12345678\"\n  instance_type = \"t3.micro\"\n  tags = { Name = \"web-server\" }\n}\n")
	r, err := ExtractFile(src, lang.HCL, "t", "main.tf")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Definitions) < 1 {
		t.Error("expected >=1 definition from HCL/Terraform")
	}
}

// --- SCSS ---
func TestSCSSRules_Regression(t *testing.T) {
	src := []byte("$primary: #007bff;\n.container {\n  width: 100%;\n  .button {\n    background: $primary;\n    &:hover { opacity: 0.8; }\n  }\n}\n")
	r, err := ExtractFile(src, lang.SCSS, "t", "styles.scss")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Definitions) < 1 {
		t.Error("expected >=1 definition from SCSS")
	}
}

// --- TOML (critical: must not be broken by config lang changes) ---
func TestTOMLBasic_Regression(t *testing.T) {
	src := []byte("[server]\nhost = \"localhost\"\nport = 8080\n\n[database]\nurl = \"postgres://localhost/db\"\nmax_connections = 10\n")
	r, err := ExtractFile(src, lang.TOML, "t", "config.toml")
	if err != nil {
		t.Fatal(err)
	}
	assertHasName(t, defsWithLabel(r, "Class"), "server")
	assertHasName(t, defsWithLabel(r, "Class"), "database")
	assertHasName(t, defsWithLabel(r, "Variable"), "host")
	assertHasName(t, defsWithLabel(r, "Variable"), "port")
}

// --- CMake ---
func TestCMakeFunction_Regression(t *testing.T) {
	src := []byte("cmake_minimum_required(VERSION 3.16)\nproject(MyApp VERSION 1.0)\nadd_executable(myapp main.cpp)\ntarget_compile_features(myapp PRIVATE cxx_std_17)\n")
	r, err := ExtractFile(src, lang.CMake, "t", "CMakeLists.txt")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Definitions) < 1 {
		t.Error("expected >=1 definition from CMakeLists.txt")
	}
}

// --- JSON ---
func TestJSONObject_Regression(t *testing.T) {
	src := []byte("{\"name\": \"myapp\", \"version\": \"1.0.0\", \"scripts\": {\"build\": \"go build\", \"test\": \"go test ./...\"}}")
	r, err := ExtractFile(src, lang.JSON, "t", "config.json")
	if err != nil {
		t.Fatal(err)
	}
	vars := defsWithLabel(r, "Variable")
	assertHasName(t, vars, "name")
	assertHasName(t, vars, "version")
}

// --- Protobuf ---
func TestProtobufMessage_Regression(t *testing.T) {
	src := []byte("syntax = \"proto3\";\npackage user;\nmessage User { int64 id = 1; string name = 2; string email = 3; }\nservice UserService { rpc GetUser(User) returns (User); }\n")
	r, err := ExtractFile(src, lang.Protobuf, "t", "user.proto")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Definitions) < 1 {
		t.Error("expected >=1 definition from Protobuf")
	}
}
