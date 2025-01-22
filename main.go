package main

import (
	_ "embed"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os/exec"
	"sync"
	"time"
)

type holder struct {
	addr  netip.Addr
	count int
	since int64
}

type env struct {
	mutex  *sync.Mutex
	holder *holder
	now    func() time.Time
}

func newEnv() *env {
	return &env{
		mutex:  new(sync.Mutex),
		holder: nil,
		now:    time.Now,
	}
}

func (e *env) join(addr netip.Addr) bool {
	e.mutex.Lock()
	defer e.mutex.Unlock()

	if e.holder != nil && e.holder.addr != addr {
		return false
	}

	if e.holder == nil {
		e.holder = &holder{
			addr:  addr,
			count: 1,
			since: e.now().UnixMilli(),
		}
		return true
	}

	e.holder.count = e.holder.count + 1
	return true
}

func (e *env) leave(addr netip.Addr) {
	e.mutex.Lock()
	defer e.mutex.Unlock()

	if e.holder == nil || e.holder.addr != addr {
		return
	}

	if e.holder.count < 2 {
		e.holder = nil
		return
	}

	e.holder.count = e.holder.count - 1
}

type uses struct {
	Name     string `json:"name"`
	Computer string `json:"computer"`
	Since    int64  `json:"since"`
}

type who struct {
	Uses *uses `json:"uses,omitempty"`
}

type tailscaleWhoisResultNode struct {
	ComputedName string
}

type tailscaleWhoisResultUserProfile struct {
	DisplayName string
}

type tailscaleWhoisResult struct {
	Node        tailscaleWhoisResultNode
	UserProfile tailscaleWhoisResultUserProfile
}

func tailscaleWhois(ip string) (*tailscaleWhoisResult, error) {
	prog, err := exec.LookPath("tailscale")
	if err != nil {
		return nil, err
	}

	log.Printf("%s whois --json %s", prog, ip)
	out, err := exec.Command(prog, "whois", "--json", ip).Output()
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			if string(exit.Stderr) == "peer not found" {
				return nil, nil
			}
			log.Println(exit.Stderr)
		}
		return nil, err
	}

	var r tailscaleWhoisResult
	if err := json.Unmarshal(out, &r); err != nil {
		return nil, err
	}

	return &r, nil
}

func whoUsesWithCommand(e *env, cmd func(string) (*tailscaleWhoisResult, error)) (*who, error) {
	if e.holder == nil {
		return &who{
			Uses: nil,
		}, nil
	}

	ip := e.holder.addr.String()
	r, err := cmd(ip)
	if err != nil {
		return nil, err
	}

	if r == nil {
		uses := uses{
			Name:     ip,
			Computer: ip,
			Since:    e.holder.since,
		}
		return &who{
			Uses: &uses,
		}, nil
	}

	uses := uses{
		Name:     r.UserProfile.DisplayName,
		Computer: r.Node.ComputedName,
		Since:    e.holder.since,
	}

	return &who{
		Uses: &uses,
	}, nil
}

func (e *env) whoUses() (*who, error) {
	return whoUsesWithCommand(e, tailscaleWhois)
}

func try(err error) {
	if err != nil {
		log.Println(err)
	}
}

func serve(down *net.TCPConn, to string) error {
	try(down.SetReadBuffer(16 * 1024))
	try(down.SetWriteBuffer(16 * 1024))

	defer down.Close()

	addr, err := net.ResolveTCPAddr("tcp", to)
	if err != nil {
		return err
	}

	up, err := net.DialTCP("tcp", nil, addr)
	if err != nil {
		return err
	}

	defer up.Close()

	try(up.SetReadBuffer(16 * 1024))
	try(up.SetWriteBuffer(16 * 1024))

	done := make(chan error)
	go func() {
		defer down.CloseWrite()
		defer up.CloseRead()
		_, err := io.Copy(down, up)
		done <- err
	}()
	go func() {
		defer up.CloseWrite()
		defer down.CloseRead()
		_, err := io.Copy(up, down)
		done <- err
	}()

	for range 2 {
		if err := <-done; err != nil {
			return err
		}
	}
	return nil
}

func serveProxy(e *env, from, to string) error {
	log.Printf("Listening... %s", from)
	addr, err := net.ResolveTCPAddr("tcp", from)
	if err != nil {
		return err
	}

	l, err := net.ListenTCP("tcp", addr)
	if err != nil {
		return err
	}

	for {
		conn, err := l.AcceptTCP()

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

		go func() {
			defer log.Printf("Leave...%s", addr)
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

//go:embed index.html
var html string

func handleRoot(writer http.ResponseWriter, req *http.Request) {
	defer req.Body.Close()

	writer.Header().Add("Content-Type", "text/html")
	writer.Header().Add("Cache-Control", "max-age=604800, stale-while-revalidate=86400")
	writer.WriteHeader(200)
	writer.Write([]byte(html))
}

func newHandleApiWho(e *env) http.HandlerFunc {
	return func(writer http.ResponseWriter, req *http.Request) {
		defer req.Body.Close()

		if req.Method != "GET" && req.Method != "HEAD" {
			http.Error(writer, "", 405)
			return
		}

		r, err := e.whoUses()

		if err != nil {
			log.Println(err)
			http.Error(writer, "Internal Server Error", 500)
			return
		}

		writer.Header().Add("Content-Type", "application/json")
		writer.Header().Add("Cache-Control", "no-cache, no-store")
		writer.WriteHeader(200)
		enc := json.NewEncoder(writer)
		if err := enc.Encode(r); err != nil {
			log.Print(err)
		}
	}
}

func serveWeb(e *env, addr string) error {
	log.Printf("Listening... %s", addr)

	mux := http.NewServeMux()
	mux.HandleFunc("/", handleRoot)
	mux.Handle("/api/who", newHandleApiWho(e))
	return http.ListenAndServe(addr, mux)
}

func main() {
	var from string
	var to string
	var webaddr string

	flag.StringVar(&from, "from", "", "listen address")
	flag.StringVar(&to, "to", "", "upstream address")
	flag.StringVar(&webaddr, "web-addr", "", "Web UI Address")
	flag.Parse()

	e := newEnv()

	errChan := make(chan error)
	go func() {
		errChan <- serveProxy(e, from, to)
	}()
	if webaddr != "" {
		go func() {
			errChan <- serveWeb(e, webaddr)
		}()
	}

	if err := <-errChan; err != nil {
		log.Fatal(err)
	}
}
