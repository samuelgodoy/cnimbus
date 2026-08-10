package main

import "fmt"

// buildArgFlag implements flag.Value for a repeatable "--build-arg
// NAME=VALUE" flag, collecting every occurrence into a map for
// nimbusfile.Parse's ARG substitution.
type buildArgFlag map[string]string

func (b buildArgFlag) String() string {
	return fmt.Sprintf("%v", map[string]string(b))
}

func (b buildArgFlag) Set(s string) error {
	name, value, ok := splitOnEquals(s)
	if !ok {
		return fmt.Errorf("--build-arg requires NAME=VALUE, got %q", s)
	}
	b[name] = value
	return nil
}

func splitOnEquals(s string) (name, value string, ok bool) {
	for i := 0; i < len(s); i++ {
		if s[i] == '=' {
			return s[:i], s[i+1:], true
		}
	}
	return "", "", false
}
