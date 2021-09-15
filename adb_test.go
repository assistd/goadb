package adb

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/zach-klippenstein/goadb/wire"
)

func TestGetServerVersion(t *testing.T) {
	s := &MockServer{
		Status:   wire.StatusSuccess,
		Messages: []string{"000a"},
	}
	client := &Adb{s}

	v, err := client.ServerVersion()
	assert.Equal(t, "host:version", s.Requests[0])
	assert.NoError(t, err)
	assert.Equal(t, 10, v)
}

func ExecCommand(commandName string, params []string) bool {

	f, err := os.OpenFile("syslog.txt", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		log.Fatalf("capture log failed: ", err)
	}

	cmd := exec.Command(commandName, params...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		log.Fatalf("%v", err)
		return false
	}
	cmd.Start()
	reader := bufio.NewReader(stdout)

	for {
		line, err2 := reader.ReadString('\n')
		if err2 != nil || io.EOF == err2 {
			break
		}
		fmt.Printf("%v", line)
		f.WriteString(line)
	}
	f.Close()
	cmd.Wait()
	return true
}