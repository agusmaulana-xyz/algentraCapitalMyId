package httpapi

import "testing"

func TestParseMQ5InputsAndValidation(t *testing.T) {
	source := []byte("input double Lots = 0.10; // lot size\r\nsinput string Tag = \"abc\";\r\ninput group \"Risk\"\r\ninput int Magic=7;\r\n// input int Commented = 1;\r\n")
	inputs := parseMQ5Inputs(source)
	if len(inputs) != 3 || inputs[0].Name != "Lots" || inputs[0].Default != "0.10" || inputs[0].Comment != "lot size" || inputs[1].Default != "abc" || inputs[2].Name != "Magic" {
		t.Fatalf("unexpected inputs: %+v", inputs)
	}
	if !validCustomEAParams(map[string]string{"Lots": "0.2"}, inputs) {
		t.Fatal("known parameter rejected")
	}
	if validCustomEAParams(map[string]string{"Unknown": "1"}, inputs) || validCustomEAParams(map[string]string{"Lots": "1\n2"}, inputs) || validCustomEAParams(map[string]string{"bad name": "1"}, nil) {
		t.Fatal("invalid parameter accepted")
	}
	if !validCustomEAParams(map[string]string{"Anything": "1"}, nil) {
		t.Fatal("free-form parameter rejected for compiled EA")
	}
}
