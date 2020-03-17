package proxy

import (
	"testing"
)

func TestExpandVars(t *testing.T) {
	testCases := []struct {
		vars   map[string]string
		value  string
		expect string
	}{
		{map[string]string{"X": "foo", "Y": "bar"},
			"${X}/${Y}", "foo/bar"},
		{map[string]string{"NAME": "X", "Y": "bar"},
			"${NAME}/${Y}", "X/bar"},
		{map[string]string{"X": "foo", "Y": "bar"},
			"${NAME}/${Y}", "${NAME}/bar"},
		{map[string]string{"NAME": "a"},
			"${NAME}/bar", "a/bar"},
		{map[string]string{"X": "foo", "Y": "bar"},
			"a/${X}/cd/${Y}", "a/foo/cd/bar"},
	}
	for _, test := range testCases {
		actual := ExpandVars(test.vars, test.value)
		if actual != test.expect {
			t.Errorf("Expected %s, got %s", test.expect, actual)
		}
	}
}
