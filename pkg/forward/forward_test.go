package forward

import (
	"bufio"
	"net"
	"testing"
)

func TestRelaysBothWays(t *testing.T) {
	backend, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	go func() {
		c, err := backend.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		line, _ := bufio.NewReader(c).ReadString('\n')
		_, _ = c.Write([]byte("echo " + line))
	}()

	// Pick a free port for the forwarder.
	probe, _ := net.Listen("tcp", "127.0.0.1:0")
	listen := probe.Addr().String()
	probe.Close()
	if err := Start(listen + "=" + backend.Addr().String()); err != nil {
		t.Fatal(err)
	}

	c, err := net.Dial("tcp", listen)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, _ = c.Write([]byte("hi\n"))
	got, _ := bufio.NewReader(c).ReadString('\n')
	if got != "echo hi\n" {
		t.Fatalf("got %q", got)
	}
}

func TestRejectsBadSpec(t *testing.T) {
	if err := Start("127.0.0.1:1"); err == nil {
		t.Fatal("no error for a pair without target")
	}
}
