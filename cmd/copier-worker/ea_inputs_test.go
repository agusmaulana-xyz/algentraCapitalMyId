package main

import "testing"

func TestParseChartInputs(t *testing.T) {
	chart := "<chart>\r\nsymbol=EURUSD\r\n<expert>\r\nname=CustomEA\r\nexpertmode=33\r\n<inputs>\r\nLots=0.10\r\nLots,F=0\r\nLots,1=0.01\r\nUseSL=true\r\nMagic=777\r\nComment=hi there\r\n</inputs>\r\n</expert>\r\n</chart>\r\n"
	inputs, ok := parseChartInputs(chart)
	if !ok || len(inputs) != 4 {
		t.Fatalf("unexpected result: %v %+v", ok, inputs)
	}
	want := []discoveredInput{{Name: "Lots", Type: "double", Default: "0.10"}, {Name: "UseSL", Type: "bool", Default: "true"}, {Name: "Magic", Type: "int", Default: "777"}, {Name: "Comment", Type: "string", Default: "hi there"}}
	for i := range want {
		if inputs[i] != want[i] {
			t.Fatalf("input %d = %+v, want %+v", i, inputs[i], want[i])
		}
	}
	if _, ok := parseChartInputs("<chart>\r\nsymbol=EURUSD\r\n</chart>"); ok {
		t.Fatal("chart without expert accepted")
	}
	loose, ok := parseChartInputs("<expert>\r\nname=X\r\npath=Experts\\X.ex5\r\nexpertmode=33\r\nLots=0.5\r\nUseSL=false\r\n</expert>")
	if !ok || len(loose) != 2 || loose[0].Name != "Lots" || loose[1].Type != "bool" {
		t.Fatalf("loose inputs not parsed: %+v", loose)
	}
	if inputs, ok := parseChartInputs("<expert>\r\nname=X\r\n</expert>"); !ok || len(inputs) != 0 {
		t.Fatal("expert without inputs should be an empty list")
	}
}
