package openai

import (
	"reflect"
	"testing"
)

func TestStorageComponents(t *testing.T) {
	base := New(Config{Model: "text-embedding-3-large"}, nil, nil).StorageComponents()
	if want := []string{"openai", "text_embedding_3_large"}; !reflect.DeepEqual(base, want) {
		t.Errorf("StorageComponents = %v, want %v", base, want)
	}
	// Explicit Matryoshka dimension becomes a trailing path segment.
	dim := New(Config{Model: "text-embedding-3-large", Dimensions: 256}, nil, nil).StorageComponents()
	if want := []string{"openai", "text_embedding_3_large", "256"}; !reflect.DeepEqual(dim, want) {
		t.Errorf("StorageComponents (dim) = %v, want %v", dim, want)
	}
}
