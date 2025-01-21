package main

import (
	"errors"
	"flag"
	"io"
	"log"
	"net"
	"net/netip"
	"sync"
)

type env struct {
	mutex *sync.Mutex;
	counter map[netip.Addr]int;
}

func (e *env) join(addr netip.Addr) bool {
	e.mutex.Lock()
	defer e.mutex.Unlock()

	val, ok := e.counter[addr]
	if !ok && len(e.counter) > 0 {
		return false
	}

	e.counter[addr] = val + 1
	return true
}

func (e *env) leave(addr netip.Addr) {
	e.mutex.Lock()
	defer e.mutex.Unlock()

	val, ok := e.counter[addr]
	if !ok {
		return
	}

	val = val - 1
	if val < 1 {
		delete(e.counter, addr)
	} else {
		e.counter[addr] = val
	}
}

func serve(down net.Conn, to string) error {
	up, err := net.Dial("tcp", to)
	if err != nil {
		down.Close()
		return err
	}

	done := make(chan error)
	go func() {
		defer down.Close()
		_, err := io.Copy(down, up)
		done <- err
	}()
	go func() {
		defer up.Close()
		_, err := io.Copy(up, down)
		done <- err
	}()

	for range 2 {
		if err := <- done; err != nil {
			return err
		}
	}
	return nil
}

func serve_proxy(e *env, from, to string) error {
	log.Printf("Listening... %s", from)
	l, err := net.Listen("tcp", from)
	if err != nil {
		return err
	}

	for {
		conn, err := l.Accept()

		if err != nil {
			return err
		}

		r, ok := conn.RemoteAddr().(*net.TCPAddr)
		if !ok {
			_ = conn.Close()
			continue
		}
		// Unmap -> ::ffff:100.79.87.92 to 100.79.87.92
		addr := r.AddrPort().Addr().Unmap()
		log.Printf("Connected... %s", addr)

		if !e.join(addr) {
			log.Printf("Busy... %s", addr)
			_ = conn.Close()
			continue
		}

		log.Printf("Joined... %s", addr)

		go func () {
			defer e.leave(addr)

			err := serve(conn, to)
			if err != nil && !errors.Is(err, net.ErrClosed) {
				log.Printf("Error...%s", err)
				return
			}

			log.Printf("Done... %s", addr)
		}()
	}
}

func main() {
	var from string
	var to string

	flag.StringVar(&from, "from", "", "listen address")
	flag.StringVar(&to, "to", "", "upstream address")
	flag.Parse()

	e := env {
		mutex: new(sync.Mutex),
		counter: map[netip.Addr]int {},
	}
	serve_proxy(&e, from, to)
}
