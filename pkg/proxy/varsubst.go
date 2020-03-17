package proxy

import (
	"regexp"
)

var (
	reVar = regexp.MustCompile(`\$\{([A-Za-z][A-Za-z0-9_]*)\}`)
)

// ExpandVars performs variable expansion of variables in the form of ${VAR}.
func ExpandVars(vars map[string]string, value string) string {
	matches := reVar.FindAllStringSubmatchIndex(value, -1)
	if len(matches) == 0 {
		return value
	}
	offset := 0
	for _, match := range matches {
		begin := match[0]
		end := match[1]
		if end < 0 {
			continue
		}
		begin += offset
		end += offset
		varName := value[match[2]+offset : match[3]+offset]
		if repl, exists := vars[varName]; exists {
			delta := len(repl) - (end - begin)
			offset += delta
			value = value[:begin] + repl + value[end:]
		}
	}
	return value
}
