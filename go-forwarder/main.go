package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"

	"golang.org/x/net/proxy"
)

var shutdownCh = make(chan struct{})
var shutdownWg = &sync.WaitGroup{}

func main() {
	// Parse command-line arguments
	flag.Parse()
	args := flag.Args()

	// Check if the number of arguments is correct
	if len(args) == 0 {
		printUsageAndExit()
	}

	for _, arg := range args {
		parts := strings.Split(arg, ",")
		if len(parts) != 3 {
			log.Printf("Invalid argument format: %s\n", arg)
			printUsageAndExit()
		}

		name := parts[0]
		listenURI := parts[1]
		connectURI := parts[2]

		// Validate URIs
		if err := checkURI(listenURI, true); err != nil {
			log.Printf("[%s] Invalid listen URI: %v\n", name, err)
			printUsageAndExit()
		}
		if err := checkURI(connectURI, false); err != nil {
			log.Printf("[%s] Invalid connect URI: %v\n", name, err)
			printUsageAndExit()
		}

		go startForwarder(name, listenURI, connectURI)
	}

	// Wait for signal to shutdown
	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGINT, syscall.SIGTERM)
	<-c
	close(shutdownCh)
	shutdownWg.Wait()
}

func startForwarder(name, listenURI, connectURI string) {
	log.Printf("[%s] %s => %s\n", name, listenURI, connectURI)

	// Create listener
	listener, cleanup := createListener(name, listenURI)
	shutdownWg.Add(1)

	// Create channel for accepting connections
	connCh := make(chan net.Conn)
	go func() {
		for {
			conn, err := listener.Accept()
			select {
			case <-shutdownCh:
				return
			default:
				if err != nil {
					log.Printf("[%s] Failed to accept connection: %v\n", name, err)
					continue
				}
				connCh <- conn
			}
		}
	}()

	// Create connector
	connector := createConnector(name, connectURI)

	for {
		select {
		case <-shutdownCh:
			cleanup()
			shutdownWg.Done()
			return
		case conn := <-connCh:
			go handleConnection(name, conn, connector)
		}
	}
}

func checkURI(uri string, isListen bool) error {
	parsedURI, err := url.Parse(uri)
	if err != nil {
		return err
	}

	switch parsedURI.Scheme {
	case "tcp":
		if parsedURI.Host == "" {
			return fmt.Errorf("missing host")
		}
	case "unix":
		if parsedURI.Path == "" {
			return fmt.Errorf("missing path")
		}
	case "socks5":
		if isListen {
			return fmt.Errorf("could not listen on a SOCKS5 URI")
		}
		if parsedURI.Host == "" {
			return fmt.Errorf("missing SOCKS5 host")
		}
		if parsedURI.Path == "" {
			return fmt.Errorf("missing SOCKS5 target")
		}
	default:
		return fmt.Errorf("unsupported scheme: %s", parsedURI.Scheme)
	}

	return nil
}

func createListener(name, uri string) (net.Listener, func()) {
	parsedURI, err := url.Parse(uri)
	if err != nil {
		panic(fmt.Sprintf("[%s] Failed to parse URI: %v", name, err))
	}

	var listener net.Listener
	var cleanup func()

	switch parsedURI.Scheme {
	case "tcp":
		listener, err = net.Listen("tcp", parsedURI.Host)
		cleanup = func() {
			listener.Close()
		}
	case "unix":
		// Delete the socket file if it already exists
		os.Remove(parsedURI.Path)
		listener, err = net.Listen("unix", parsedURI.Path)
		cleanup = func() {
			listener.Close()
			os.Remove(parsedURI.Path)
		}
	default:
		panic(fmt.Sprintf("[%s] Unsupported scheme: %s", name, parsedURI.Scheme))
	}

	if err != nil {
		panic(fmt.Sprintf("[%s] Failed to create listener: %v", name, err))
	}

	return listener, cleanup
}

func createConnector(name, uri string) func() (net.Conn, error) {
	parsedURI, err := url.Parse(uri)
	if err != nil {
		panic(fmt.Sprintf("[%s] Failed to parse URI: %v", name, err))
	}

	switch parsedURI.Scheme {
	case "tcp":
		return func() (net.Conn, error) {
			return net.Dial("tcp", parsedURI.Host)
		}
	case "unix":
		return func() (net.Conn, error) {
			return net.Dial("unix", parsedURI.Path)
		}
	case "socks5":
		return func() (net.Conn, error) {
			dialer, err := proxy.SOCKS5("tcp", parsedURI.Host, nil, proxy.Direct)
			if err != nil {
				return nil, fmt.Errorf("[%s] failed to create SOCKS5 dialer: %v", name, err)
			}
			return dialer.Dial("tcp", parsedURI.Path[1:])
		}
	default:
		panic(fmt.Sprintf("[%s] Unsupported scheme: %s", name, parsedURI.Scheme))
	}
}

func handleConnection(name string, conn net.Conn, connector func() (net.Conn, error)) {
	targetConn, err := connector()
	if err != nil {
		conn.Close()
		log.Printf("[%s] Failed to connect to target: %v\n", name, err)
		return
	}

	var closed uint32 = 0
	close := func() {
		if atomic.CompareAndSwapUint32(&closed, 0, 1) {
			conn.Close()
			targetConn.Close()
		}
	}

	// Forward data between conn and targetConn
	go func() {
		defer close()

		_, err := io.Copy(targetConn, conn)
		if err != nil && atomic.LoadUint32(&closed) == 0 {
			log.Printf("[%s] Error copying data to target: %v\n", name, err)
		}
		conn.Close()
	}()

	defer close()

	_, err = io.Copy(conn, targetConn)
	if err != nil && atomic.LoadUint32(&closed) == 0 {
		log.Printf("[%s] Error copying data from target: %v\n", name, err)
	}
}

func printUsageAndExit() {
	fmt.Println("Usage: go-forwarder <name>,<listen>,<connect> [<name>,<listen>,<connect> ...]")
	fmt.Println("  <name> is the instance name of each forwarder")
	fmt.Println("  <listen> and <connect> can be one of the following URIs:")
	fmt.Println("    - TCP URI: e.g. tcp://127.0.0.1:8080")
	fmt.Println("    - UNIX socket URI: e.g. unix:/run/my-service.sock")
	fmt.Println("    - SOCKS5 URI (only for connect): e.g. socks5://127.0.0.1:1055/192.168.1.1:80")
	os.Exit(1)
}
