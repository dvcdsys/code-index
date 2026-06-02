package voyage

import (
	"reflect"
	"testing"
)

func TestStorageComponents(t *testing.T) {
	got := New(Config{Model: "voyage-code-3", OutputDimension: 2048, OutputDtype: "int8"}, nil, nil).StorageComponents()
	if want := []string{"voyage", "voyage_code_3", "2048_int8"}; !reflect.DeepEqual(got, want) {
		t.Errorf("StorageComponents = %v, want %v", got, want)
	}
	// Unset dimension → "auto" variant prefix (mirrors ID()).
	auto := New(Config{Model: "voyage-3", OutputDtype: "float"}, nil, nil).StorageComponents()
	if want := []string{"voyage", "voyage_3", "auto_float"}; !reflect.DeepEqual(auto, want) {
		t.Errorf("StorageComponents (auto) = %v, want %v", auto, want)
	}
}
