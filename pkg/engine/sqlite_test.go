package engine

import (
	"testing"
)

func newTestStore(t *testing.T) *SQLiteStore {
	t.Helper()
	store, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestSQLiteStore_UpsertAndSearchSymbol(t *testing.T) {
	store := newTestStore(t)

	chunk := Chunk{Name: "MyFunc", Kind: "function", File: "main.go", Line: 10, Content: "func MyFunc() {}"}
	if err := store.Upsert(chunk, "hash1"); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	results, err := store.SearchSymbol("MyFunc", 10)
	if err != nil {
		t.Fatalf("SearchSymbol: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Name != "MyFunc" {
		t.Errorf("expected Name=MyFunc, got %q", results[0].Name)
	}
	if results[0].Kind != "function" {
		t.Errorf("expected Kind=function, got %q", results[0].Kind)
	}
}

func TestSQLiteStore_UpsertIdempotent(t *testing.T) {
	store := newTestStore(t)

	chunk := Chunk{Name: "Foo", Kind: "function", File: "a.go", Line: 1, Content: "func Foo() {}"}
	if err := store.Upsert(chunk, "hash-foo"); err != nil {
		t.Fatalf("first Upsert: %v", err)
	}
	// same hash — should update, not insert duplicate
	if err := store.Upsert(chunk, "hash-foo"); err != nil {
		t.Fatalf("second Upsert: %v", err)
	}

	results, err := store.SearchSymbol("Foo", 10)
	if err != nil {
		t.Fatalf("SearchSymbol: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("expected 1 result after duplicate upsert, got %d", len(results))
	}
}

func TestSQLiteStore_SearchSymbol_CaseInsensitive(t *testing.T) {
	store := newTestStore(t)

	chunk := Chunk{Name: "ParseFile", Kind: "function", File: "parser.go", Line: 5, Content: "func ParseFile() {}"}
	if err := store.Upsert(chunk, "hash-parse"); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	results, err := store.SearchSymbol("parsefile", 10)
	if err != nil {
		t.Fatalf("SearchSymbol: %v", err)
	}
	if len(results) == 0 {
		t.Error("expected case-insensitive match, got 0 results")
	}
}

func TestSQLiteStore_SearchSymbol_NoResults(t *testing.T) {
	store := newTestStore(t)

	results, err := store.SearchSymbol("nonexistent", 10)
	if err != nil {
		t.Fatalf("SearchSymbol: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
}

func TestSQLiteStore_SearchTrigram(t *testing.T) {
	store := newTestStore(t)

	chunk := Chunk{Name: "QdrantStore", Kind: "struct", File: "qdrant.go", Line: 11, Content: "type QdrantStore struct { client *qdrant.Client }"}
	if err := store.Upsert(chunk, "hash-qdrant"); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	results, err := store.SearchTrigram("Qdrant", 10)
	if err != nil {
		t.Fatalf("SearchTrigram: %v", err)
	}
	if len(results) == 0 {
		t.Error("expected trigram match for 'Qdrant', got 0 results")
	}
}

func TestSQLiteStore_SearchSymbol_Limit(t *testing.T) {
	store := newTestStore(t)

	for i := 0; i < 5; i++ {
		c := Chunk{Name: "Func", Kind: "function", File: "f.go", Line: i + 1, Content: "func Func() {}"}
		hash := "hash-" + string(rune('a'+i))
		if err := store.Upsert(c, hash); err != nil {
			t.Fatalf("Upsert %d: %v", i, err)
		}
	}

	results, err := store.SearchSymbol("Func", 3)
	if err != nil {
		t.Fatalf("SearchSymbol: %v", err)
	}
	if len(results) > 3 {
		t.Errorf("expected at most 3 results, got %d", len(results))
	}
}
