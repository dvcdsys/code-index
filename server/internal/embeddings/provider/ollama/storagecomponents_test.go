package ollama

import (
	"reflect"
	"testing"
)

func TestStorageComponents(t *testing.T) {
	got := New(Config{Model: "nomic-embed-text"}, nil).StorageComponents()
	if want := []string{"ollama", "nomic_embed_text"}; !reflect.DeepEqual(got, want) {
		t.Errorf("StorageComponents = %v, want %v", got, want)
	}
}

// TestStorageComponents_KindNotGluedToModel is the #8 anti-collision guard:
// the provider kind is its own path segment, so a model literally named
// "ollama-foo" (slug "ollama_foo") never shares a namespace with model
// "foo". Under the old flat single-slug scheme both collapsed to
// "chroma_ollama_foo".
func TestStorageComponents_KindNotGluedToModel(t *testing.T) {
	foo := New(Config{Model: "foo"}, nil).StorageComponents()
	ollamaFoo := New(Config{Model: "ollama-foo"}, nil).StorageComponents()
	if reflect.DeepEqual(foo, ollamaFoo) {
		t.Fatalf("distinct models must not share a path: %v == %v", foo, ollamaFoo)
	}
	if got, want := ollamaFoo[len(ollamaFoo)-1], "ollama_foo"; got != want {
		t.Errorf("model slug = %q, want %q", got, want)
	}
}
