package adb

import (
	"fmt"
	"reflect"
	"regexp"
	"strings"

	"github.com/zach-klippenstein/goadb/internal/errors"
)

var (
	whitespaceRegex = regexp.MustCompile(`^\s*$`)
)

func containsWhitespace(str string) bool {
	return strings.ContainsAny(str, " \t\v")
}

func isBlank(str string) bool {
	return whitespaceRegex.MatchString(str)
}

func wrapClientError(err error, client interface{}, operation string, args ...interface{}) error {
	if err == nil {
		return nil
	}
	if _, ok := err.(*errors.Err); !ok {
		panic("err is not a *Err: " + err.Error())
	}

	clientType := reflect.TypeOf(client)

	return &errors.Err{
		Code:    err.(*errors.Err).Code,
		Cause:   err,
		Message: fmt.Sprintf("error performing %s on %s", fmt.Sprintf(operation, args...), clientType),
		Details: client,
	}
}

func deleteExtraSpace(s string) string {
	s1 := strings.Replace(s, "  ", " ", -1)
	regstr := "\\s{2,}"
	reg, _ := regexp.Compile(regstr)
	s2 := make([]byte, len(s1))
	copy(s2, s1)
	spc_index := reg.FindStringIndex(string(s2))
	for len(spc_index) > 0 {
		s2 = append(s2[:spc_index[0]+1], s2[spc_index[1]:]...)
		spc_index = reg.FindStringIndex(string(s2))
	}
	return string(s2)
}
