package engine

import (
	"testing"
)

func TestRuntimeValueAsKubernetesJSONDecimal(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		text string
		want float64
		fail bool
	}{
		{name: "histogram bucket", text: "0.01", want: 0.01},
		{name: "fraction", text: "0.1", want: 0.1},
		{name: "trailing zeroes", text: "0.1000", want: 0.1},
		{name: "exponent", text: "1e-5", want: 1e-5},
		{name: "precision loss", text: "0.100000000000000001", fail: true},
		{name: "overflow", text: "1e400", fail: true},
		{name: "underflow", text: "1e-400", fail: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			value, err := runtimeValueAsKubernetesJSON(RuntimeValue{Value: runtimeValueData{Kind: Decimal, Text: test.text}})
			if test.fail {
				if err == nil {
					t.Fatal("expected a numeric conversion error")
				}

				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if value != test.want {
				t.Fatalf("converted value = %v, want %v", value, test.want)
			}
		})
	}
}
