package nilcheck_test

import (
	"bytes"
	"io"
	"testing"

	"go.krak3n.dev/foundation/internal/nilcheck"
)

type named *int

func TestIs(t *testing.T) {
	var (
		nilPtr   *int
		nilMap   map[string]int
		nilSlice []int
		nilChan  chan int
		nilFunc  func()
		nilNamed named
		nilBuf   io.Reader = (*bytes.Buffer)(nil)
		nilErr   error
		x        = 1
	)

	tests := map[string]struct {
		v    any
		want bool
	}{
		"the nil interface":                  {nil, true},
		"a nil interface variable":           {nilErr, true},
		"a nil pointer":                      {nilPtr, true},
		"a nil pointer in an interface type": {nilBuf, true},
		"a nil named pointer type":           {nilNamed, true},
		"a nil map":                          {nilMap, true},
		"a nil slice":                        {nilSlice, true},
		"a nil channel":                      {nilChan, true},
		"a nil function":                     {nilFunc, true},

		"a pointer":            {&x, false},
		"an empty map":         {map[string]int{}, false},
		"an empty slice":       {[]int{}, false},
		"a channel":            {make(chan int), false},
		"a function":           {func() {}, false},
		"a buffer":             {&bytes.Buffer{}, false},
		"a struct":             {struct{}{}, false},
		"zero int":             {0, false},
		"an empty string":      {"", false},
		"false":                {false, false},
		"a zero array":         {[0]int{}, false},
		"a struct holding nil": {struct{ P *int }{}, false},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if got := nilcheck.Is(tt.v); got != tt.want {
				t.Errorf("Is(%#v) = %v, want %v", tt.v, got, tt.want)
			}
		})
	}
}
