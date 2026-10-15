package hdc

import (
	"reflect"
	"testing"
)

func TestParseTargets(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "[Empty]\n", []string{}},
		{"single", "1234567890\n", []string{"1234567890"}},
		{"multiple", "dev1\ndev2\n\n", []string{"dev1", "dev2"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseTargets(tt.in); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseTargets(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestParsePorts(t *testing.T) {
	t.Run("forward", func(t *testing.T) {
		in := "dev1 tcp:9000 tcp:8000 [Forward]\ndev2 tcp:9222 tcp:9223 [Forward]\n"
		want := []Forward{
			{Target: "dev1", Local: "tcp:9000", Remote: "tcp:8000"},
			{Target: "dev2", Local: "tcp:9222", Remote: "tcp:9223"},
		}
		if got := parsePorts(in, false); !reflect.DeepEqual(got, want) {
			t.Errorf("parsePorts forward = %v, want %v", got, want)
		}
	})
	t.Run("reverse", func(t *testing.T) {
		in := "dev1 tcp:8000 tcp:9000 [Reverse]\n"
		want := []Forward{{Target: "dev1", Local: "tcp:9000", Remote: "tcp:8000"}}
		if got := parsePorts(in, true); !reflect.DeepEqual(got, want) {
			t.Errorf("parsePorts reverse = %v, want %v", got, want)
		}
	})
	t.Run("empty", func(t *testing.T) {
		if got := parsePorts("[Empty]\n", false); len(got) != 0 {
			t.Errorf("expected no forwards, got %v", got)
		}
	})
}

func TestParseParameters(t *testing.T) {
	in := "const.product.name = OpenHarmony\r\n" +
		"const.product.model = Mate60\n" +
		"const.product.cpu.abi = arm64\n"
	want := map[string]string{
		"const.product.name":    "OpenHarmony",
		"const.product.model":   "Mate60",
		"const.product.cpu.abi": "arm64",
	}
	got := parseParameters(in)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseParameters = %#v, want %#v", got, want)
	}
}
