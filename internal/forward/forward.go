// Package forward relays local TCP ports to other hosts. In the container,
// the platform services keep their 127.0.0.1 addresses for MariaDB and
// OpenSearch, and mini forwards those ports to the compose services, so one
// set of service configs serves both a native run and the container.
package forward

import (
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
)

// Start parses "listen=target,listen=target" (for example
// "127.0.0.1:3306=mariadb:3306") and relays each pair in the background.
func Start(spec string) error {
	for _, pair := range strings.Split(spec, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		listen, target, ok := strings.Cut(pair, "=")
		if !ok || listen == "" || target == "" {
			return fmt.Errorf("forward %q: want listen=target", pair)
		}
		ln, err := net.Listen("tcp", listen)
		if err != nil {
			return fmt.Errorf("forward %s: %w", listen, err)
		}
		go serve(ln, target)
	}
	return nil
}

func serve(ln net.Listener, target string) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go relay(conn, target)
	}
}

func relay(in net.Conn, target string) {
	defer in.Close()
	out, err := net.Dial("tcp", target)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[mini] forward to %s: %v\n", target, err)
		return
	}
	defer out.Close()
	var wg sync.WaitGroup
	wg.Add(2)
	pipe := func(dst, src net.Conn) {
		defer wg.Done()
		_, _ = io.Copy(dst, src)
		if tc, ok := dst.(*net.TCPConn); ok {
			_ = tc.CloseWrite()
		}
	}
	go pipe(out, in)
	go pipe(in, out)
	wg.Wait()
}
