package protocol

import (
	"reflect"
	"testing"

	"github.com/SnimanOff/UTBPanel/backend/sdk/wire"
)

func TestStorageClasses(t *testing.T) {
	for _, v := range []any{nil, int64(-42), float64(1.25), "текст", []byte{0, 255}, []byte{}} {
		encoded, err := Encode(v)
		if err != nil {
			t.Fatal(err)
		}
		data, err := wire.Marshal(encoded)
		if err != nil {
			t.Fatal(err)
		}
		var remote Value
		if err := wire.Unmarshal(data, &remote); err != nil {
			t.Fatal(err)
		}
		got, err := remote.Decode()
		if err != nil || !reflect.DeepEqual(v, got) {
			t.Fatalf("%#v -> %#v: %v", v, got, err)
		}
	}
}
