package engine

import (
	"testing"
)

func TestParseFile_UnsupportedLanguage(t *testing.T) {
	_, err := ParseFile([]byte(`print("hi")`), "ruby")
	if err == nil {
		t.Fatal("expected error for unsupported language")
	}
}

func TestParseFile_Go_Functions(t *testing.T) {
	src := []byte(`package main

func Hello() string { return "hello" }
func World() string { return "world" }
`)
	chunks, err := ParseFile(src, "go")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	names := chunkNames(chunks)
	assertContains(t, names, "Hello")
	assertContains(t, names, "World")
}

func TestParseFile_Go_Method(t *testing.T) {
	src := []byte(`package main

type Foo struct{}

func (f *Foo) Bar() {}
`)
	chunks, err := ParseFile(src, "go")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	names := chunkNames(chunks)
	assertContains(t, names, "Foo.Bar")
}

func TestParseFile_Go_Struct(t *testing.T) {
	src := []byte(`package main

type MyStruct struct {
	Name string
}
`)
	chunks, err := ParseFile(src, "go")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	names := chunkNames(chunks)
	assertContains(t, names, "MyStruct")
	assertKind(t, chunks, "MyStruct", "struct")
}

func TestParseFile_Go_Interface(t *testing.T) {
	src := []byte(`package main

type Reader interface {
	Read(p []byte) (int, error)
}
`)
	chunks, err := ParseFile(src, "go")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertKind(t, chunks, "Reader", "interface")
}

func TestParseFile_Go_LineNumbers(t *testing.T) {
	src := []byte(`package main

func First() {}

func Second() {}
`)
	chunks, err := ParseFile(src, "go")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, c := range chunks {
		if c.Line <= 0 {
			t.Errorf("chunk %q has invalid line %d", c.Name, c.Line)
		}
	}
}

func TestParseFile_TypeScript_Function(t *testing.T) {
	src := []byte(`function greet(name: string): string {
	return "hello " + name;
}
`)
	chunks, err := ParseFile(src, "typescript")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertContains(t, chunkNames(chunks), "greet")
}

func TestParseFile_TypeScript_Class(t *testing.T) {
	src := []byte(`class Animal {
	name: string;
	constructor(name: string) { this.name = name; }
}
`)
	chunks, err := ParseFile(src, "typescript")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertKind(t, chunks, "Animal", "class")
}

func TestParseFile_TypeScript_Interface(t *testing.T) {
	src := []byte(`interface Shape {
	area(): number;
}
`)
	chunks, err := ParseFile(src, "typescript")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertKind(t, chunks, "Shape", "interface")
}

func TestParseFile_TypeScript_ArrowFunction(t *testing.T) {
	src := []byte(`const add = (a: number, b: number): number => a + b;
`)
	chunks, err := ParseFile(src, "typescript")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertContains(t, chunkNames(chunks), "add")
}

func TestParseFile_Python_Function(t *testing.T) {
	src := []byte(`def hello(name):
    return "hello " + name
`)
	chunks, err := ParseFile(src, "python")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertContains(t, chunkNames(chunks), "hello")
}

func TestParseFile_Python_Class(t *testing.T) {
	src := []byte(`class Dog:
    def bark(self):
        print("woof")
`)
	chunks, err := ParseFile(src, "python")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertKind(t, chunks, "Dog", "class")
}

func TestSupportedLanguages(t *testing.T) {
	langs := SupportedLanguages()
	expected := []string{"go", "typescript", "tsx", "python"}
	for _, want := range expected {
		found := false
		for _, got := range langs {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected language %q in SupportedLanguages", want)
		}
	}
}

// --- helpers ---

func chunkNames(chunks []Chunk) []string {
	names := make([]string, len(chunks))
	for i, c := range chunks {
		names[i] = c.Name
	}
	return names
}

func assertContains(t *testing.T, names []string, want string) {
	t.Helper()
	for _, n := range names {
		if n == want {
			return
		}
	}
	t.Errorf("expected chunk %q, got %v", want, names)
}

func assertKind(t *testing.T, chunks []Chunk, name, wantKind string) {
	t.Helper()
	for _, c := range chunks {
		if c.Name == name {
			if c.Kind != wantKind {
				t.Errorf("chunk %q: expected kind %q, got %q", name, wantKind, c.Kind)
			}
			return
		}
	}
	t.Errorf("chunk %q not found", name)
}
