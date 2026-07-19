// ============================================================
// GoDnsProxy - Smart DNS Proxy with HTTP/3 and SOCKS5 support
// ============================================================
// Author: aminranjibar2007
// Repository: https://github.com/aminranjibar2007/GoDnsProxy
// License: GNU General Public License v3.0
// ============================================================
// This program is a smart DNS proxy that redirects all DNS requests
// to a specified IP address and proxies HTTP/HTTPS traffic as well.
// ============================================================

package main

import (
    "crypto/tls"
    "io"
    "log"
    "net"
    "net/http"
    "strings"
    "time"
    
    "github.com/lucas-clemente/quic-go"
    "github.com/lucas-clemente/quic-go/http3"
    "golang.org/x/net/proxy"
)

// ============================================================
// Configuration section (user-modifiable)
// ============================================================

var (
    // SystemIP: The IP address that all DNS requests will be redirected to
    // This should be your server's public or private IP address
    SystemIP      = "192.168.1.100"
    
    // SocksServer: SOCKS5 server address for outgoing traffic (optional)
    SocksServer   = "192.168.1.50"
    
    // SocksPort: SOCKS5 server port
    SocksPort     = "1080"
    
    // UseSocksProxy: Automatically set based on SocksServer values
    UseSocksProxy = false
)

// ============================================================
// init function: Auto-enable SOCKS if server info is provided
// ============================================================

func init() {
    if SocksServer != "" && SocksPort != "" {
        UseSocksProxy = true
        log.Printf("SOCKS5 proxy enabled: %s:%s", SocksServer, SocksPort)
    } else {
        log.Println("SOCKS5 proxy disabled - using direct connections")
    }
}

// ============================================================
// main function: Start all services concurrently
// ============================================================

func main() {
    log.Println("========================================")
    log.Println("GoDnsProxy - Smart DNS Proxy v1.0")
    log.Println("Author: aminranjibar2007")
    log.Println("License: GNU GPL v3.0")
    log.Println("========================================")
    log.Printf("System IP for DNS responses: %s", SystemIP)
    if UseSocksProxy {
        log.Printf("Using SOCKS5 proxy: %s:%s", SocksServer, SocksPort)
    }
    log.Println("========================================")
    
    // Start DNS Server on port 53 (UDP)
    go startDNSServer(":53")
    
    // Start HTTP Proxy on port 80 (TCP)
    go startHTTPProxy(":80")
    
    // Start HTTPS Proxy on port 443 (TCP)
    go startHTTPSProxy(":443")
    
    // Start HTTP/3 Proxy on port 443 (UDP) - runs concurrently with HTTPS
    startHTTP3Proxy(":443")
}

// ============================================================
// DNS Server section: Respond to DNS requests with custom IP
// ============================================================

// startDNSServer: Start DNS server on specified UDP port
func startDNSServer(addr string) {
    udpAddr, err := net.ResolveUDPAddr("udp", addr)
    if err != nil {
        log.Fatal(err)
    }
    
    conn, err := net.ListenUDP("udp", udpAddr)
    if err != nil {
        log.Fatal(err)
    }
    defer conn.Close()
    
    log.Printf("DNS Server listening on %s", addr)
    log.Printf("All domains will resolve to: %s", SystemIP)
    
    buf := make([]byte, 512)
    for {
        n, clientAddr, err := conn.ReadFromUDP(buf)
        if err != nil {
            log.Printf("DNS read error: %v", err)
            continue
        }
        // Process each request in a separate goroutine
        go handleDNSRequest(conn, clientAddr, buf[:n])
    }
}

// handleDNSRequest: Process a DNS request and build response with custom IP
func handleDNSRequest(conn *net.UDPConn, clientAddr *net.UDPAddr, data []byte) {
    if len(data) < 12 {
        return
    }
    
    // Build response packet by copying the request
    response := make([]byte, len(data)+20)
    copy(response, data)
    
    // Set QR (Query/Response) bit to 1 to indicate response
    response[2] |= 0x80
    
    // Set number of answers (1 answer)
    response[6] = 0x00
    response[7] = 0x01
    
    // Find the start of the question section in the request
    questionStart := 12
    for i := 12; i < len(data); i++ {
        if data[i] == 0 {
            questionStart = i + 1
            break
        }
    }
    
    if questionStart+4 > len(data) {
        return
    }
    
    // Copy the question section to the response
    copy(response[12:], data[12:questionStart+4])
    
    // Build the response section with pointer to domain name
    offset := questionStart + 4
    response[offset] = 0xC0   // Pointer to domain name
    response[offset+1] = 0x0C // offset 12 (start of question)
    offset += 2
    
    // Type: A (1) - IPv4 address record
    response[offset] = 0x00
    response[offset+1] = 0x01
    offset += 2
    
    // Class: IN (1) - Internet class
    response[offset] = 0x00
    response[offset+1] = 0x01
    offset += 2
    
    // TTL: 300 seconds (5 minutes)
    response[offset] = 0x00
    response[offset+1] = 0x00
    response[offset+2] = 0x01
    response[offset+3] = 0x2C
    offset += 4
    
    // Data length: 4 bytes (IPv4 address)
    response[offset] = 0x00
    response[offset+1] = 0x04
    offset += 2
    
    // Place the custom IP address in the response
    ip := net.ParseIP(SystemIP)
    if ip == nil {
        // If IP is invalid, fallback to 127.0.0.1
        log.Printf("Invalid SystemIP '%s', falling back to 127.0.0.1", SystemIP)
        ip = net.ParseIP("127.0.0.1")
    }
    ipv4 := ip.To4()
    if ipv4 != nil {
        copy(response[offset:], ipv4)
    } else {
        // If error occurs, use 127.0.0.1
        log.Printf("IP %s is not IPv4, falling back to 127.0.0.1", SystemIP)
        response[offset] = 127
        response[offset+1] = 0
        response[offset+2] = 0
        response[offset+3] = 1
    }
    
    // Send response to client
    _, err := conn.WriteToUDP(response[:offset+4], clientAddr)
    if err != nil {
        log.Printf("DNS write error: %v", err)
    }
}

// ============================================================
// HTTP Proxy section: Proxy standard HTTP requests
// ============================================================

// startHTTPProxy: Start HTTP Proxy server on specified port
func startHTTPProxy(addr string) {
    proxy := &http.Server{
        Addr: addr,
        Handler: http.HandlerFunc(handleHTTP),
        ReadTimeout:  30 * time.Second,
        WriteTimeout: 30 * time.Second,
    }
    log.Printf("HTTP Proxy listening on %s", addr)
    log.Fatal(proxy.ListenAndServe())
}

// handleHTTP: Process HTTP and CONNECT requests
func handleHTTP(w http.ResponseWriter, r *http.Request) {
    // If request is CONNECT, forward to dedicated handler
    if r.Method == http.MethodConnect {
        handleConnect(w, r)
        return
    }
    
    // Create HTTP client with SOCKS support (if enabled)
    client := createHTTPClient()
    
    // Build destination URL
    destURL := r.URL.String()
    if !strings.HasPrefix(destURL, "http://") && !strings.HasPrefix(destURL, "https://") {
        destURL = "http://" + destURL
    }
    
    // Build new request for destination server
    req, err := http.NewRequest(r.Method, destURL, r.Body)
    if err != nil {
        http.Error(w, err.Error(), http.StatusInternalServerError)
        return
    }
    
    // Copy headers and add X-Forwarded-For header
    req.Header = r.Header.Clone()
    req.Header.Set("X-Forwarded-For", r.RemoteAddr)
    
    // Send request to destination server
    resp, err := client.Do(req)
    if err != nil {
        http.Error(w, err.Error(), http.StatusBadGateway)
        return
    }
    defer resp.Body.Close()
    
    // Copy response headers to client
    for key, values := range resp.Header {
        for _, value := range values {
            w.Header().Add(key, value)
        }
    }
    
    // Send status and response body
    w.WriteHeader(resp.StatusCode)
    io.Copy(w, resp.Body)
}

// ============================================================
// HTTPS Proxy section: Proxy CONNECT requests for HTTPS
// ============================================================

// startHTTPSProxy: Start HTTPS Proxy server on specified port
func startHTTPSProxy(addr string) {
    listener, err := net.Listen("tcp", addr)
    if err != nil {
        log.Fatal(err)
    }
    
    log.Printf("HTTPS Proxy (CONNECT) listening on %s", addr)
    
    for {
        conn, err := listener.Accept()
        if err != nil {
            log.Printf("Accept error: %v", err)
            continue
        }
        go handleHTTPSConnection(conn)
    }
}

// handleHTTPSConnection: Handle HTTPS connection using CONNECT
func handleHTTPSConnection(clientConn net.Conn) {
    defer clientConn.Close()
    
    // Read CONNECT request from client
    buf := make([]byte, 4096)
    n, err := clientConn.Read(buf)
    if err != nil {
        log.Printf("Read error: %v", err)
        return
    }
    
    request := string(buf[:n])
    lines := strings.Split(request, "\r\n")
    if len(lines) == 0 {
        return
    }
    
    firstLine := strings.Fields(lines[0])
    if len(firstLine) < 3 || firstLine[0] != "CONNECT" {
        return
    }
    
    target := firstLine[1]
    
    // Connect to target server (via SOCKS or directly)
    targetConn, err := dialWithSOCKS("tcp", target)
    if err != nil {
        log.Printf("Cannot connect to target %s: %v", target, err)
        clientConn.Write([]byte("HTTP/1.1 502 Bad Gateway\r\n\r\n"))
        return
    }
    defer targetConn.Close()
    
    // Send success response to client
    _, err = clientConn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
    if err != nil {
        log.Printf("Write error: %v", err)
        return
    }
    
    // Transfer data between both connections (bidirectional tunnel)
    go transfer(targetConn, clientConn)
    transfer(clientConn, targetConn)
}

// ============================================================
// HTTP CONNECT Handler: Handle CONNECT in HTTP Proxy
// ============================================================

// handleConnect: Handle CONNECT request in HTTP Proxy
func handleConnect(w http.ResponseWriter, r *http.Request) {
    // Connect to destination server
    destConn, err := dialWithSOCKS("tcp", r.Host)
    if err != nil {
        http.Error(w, err.Error(), http.StatusBadGateway)
        return
    }
    defer destConn.Close()
    
    w.WriteHeader(http.StatusOK)
    
    // Hijack connection for tunneling
    hijacker, ok := w.(http.Hijacker)
    if !ok {
        http.Error(w, "Hijacking not supported", http.StatusInternalServerError)
        return
    }
    
    clientConn, _, err := hijacker.Hijack()
    if err != nil {
        http.Error(w, err.Error(), http.StatusInternalServerError)
        return
    }
    defer clientConn.Close()
    
    // Transfer data between both connections
    go transfer(destConn, clientConn)
    transfer(clientConn, destConn)
}

// ============================================================
// HTTP/3 Proxy section: Proxy HTTP/3 (QUIC) requests
// ============================================================

// startHTTP3Proxy: Start HTTP/3 server on UDP port 443
func startHTTP3Proxy(addr string) {
    tlsConfig := generateTLSConfig()
    
    server := http3.Server{
        Addr:      addr,
        TLSConfig: tlsConfig,
        Handler:   http.HandlerFunc(handleHTTP3),
    }
    
    log.Printf("HTTP/3 (QUIC) Proxy listening on %s", addr)
    
    err := server.ListenAndServeQUIC("udp", addr)
    if err != nil {
        log.Fatalf("HTTP/3 server error: %v", err)
    }
}

// handleHTTP3: Process HTTP/3 requests
func handleHTTP3(w http.ResponseWriter, r *http.Request) {
    log.Printf("HTTP/3 request: %s %s from %s", r.Method, r.URL, r.RemoteAddr)
    
    // Support CONNECT in HTTP/3
    if r.Method == http.MethodConnect {
        handleHTTP3Connect(w, r)
        return
    }
    
    // Create HTTP/3 client
    client := createHTTP3Client()
    
    // Build destination URL
    destURL := r.URL.String()
    if !strings.HasPrefix(destURL, "http://") && !strings.HasPrefix(destURL, "https://") {
        destURL = "http://" + destURL
    }
    
    // Build new request
    req, err := http.NewRequest(r.Method, destURL, r.Body)
    if err != nil {
        http.Error(w, err.Error(), http.StatusInternalServerError)
        return
    }
    
    // Copy headers and add custom headers
    req.Header = r.Header.Clone()
    req.Header.Set("X-Forwarded-For", r.RemoteAddr)
    req.Header.Set("X-Forwarded-Proto", "http3")
    
    // Send request to destination server
    resp, err := client.Do(req)
    if err != nil {
        http.Error(w, err.Error(), http.StatusBadGateway)
        return
    }
    defer resp.Body.Close()
    
    // Copy response headers
    for key, values := range resp.Header {
        for _, value := range values {
            w.Header().Add(key, value)
        }
    }
    
    w.WriteHeader(resp.StatusCode)
    io.Copy(w, resp.Body)
}

// handleHTTP3Connect: Handle CONNECT in HTTP/3
func handleHTTP3Connect(w http.ResponseWriter, r *http.Request) {
    destConn, err := dialWithSOCKS("tcp", r.Host)
    if err != nil {
        http.Error(w, err.Error(), http.StatusBadGateway)
        return
    }
    defer destConn.Close()
    
    w.WriteHeader(http.StatusOK)
    
    // Hijack can also be used in HTTP/3
    hijacker, ok := w.(http.Hijacker)
    if !ok {
        http.Error(w, "Hijacking not supported for HTTP/3", http.StatusInternalServerError)
        return
    }
    
    clientConn, _, err := hijacker.Hijack()
    if err != nil {
        http.Error(w, err.Error(), http.StatusInternalServerError)
        return
    }
    defer clientConn.Close()
    
    go transfer(destConn, clientConn)
    transfer(clientConn, destConn)
}

// ============================================================
// Helper functions: Create HTTP clients with SOCKS support
// ============================================================

// createHTTPClient: Create HTTP Client with SOCKS5 support
func createHTTPClient() *http.Client {
    transport := &http.Transport{
        TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
    }
    
    if UseSocksProxy {
        socksAddr := net.JoinHostPort(SocksServer, SocksPort)
        dialer, err := proxy.SOCKS5("tcp", socksAddr, nil, proxy.Direct)
        if err != nil {
            log.Printf("Failed to create SOCKS5 dialer: %v", err)
        } else {
            transport.DialContext = dialer.(proxy.ContextDialer).DialContext
            log.Printf("Using SOCKS5 proxy: %s", socksAddr)
        }
    }
    
    return &http.Client{
        Timeout:   30 * time.Second,
        Transport: transport,
    }
}

// createHTTP3Client: Create HTTP/3 Client with SOCKS5 support
func createHTTP3Client() *http.Client {
    transport := &http3.RoundTripper{
        TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
    }
    
    if UseSocksProxy {
        log.Printf("HTTP/3 with SOCKS5 proxy: %s", net.JoinHostPort(SocksServer, SocksPort))
        // For HTTP/3, we need a custom Dialer with SOCKS support
        transport = &http3.RoundTripper{
            TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
            Dial: func(addr string, tlsCfg *tls.Config, cfg *quic.Config) (quic.EarlyConnection, error) {
                // Connect via SOCKS5 to target server
                conn, err := dialWithSOCKS("udp", addr)
                if err != nil {
                    return nil, err
                }
                // Convert to UDP connection for QUIC
                udpConn, ok := conn.(*net.UDPConn)
                if !ok {
                    return nil, err
                }
                return quic.DialEarly(udpConn, addr, tlsCfg, cfg)
            },
        }
    }
    
    return &http.Client{
        Timeout:   30 * time.Second,
        Transport: transport,
    }
}

// dialWithSOCKS: Connect to destination via SOCKS5 or directly
func dialWithSOCKS(network, addr string) (net.Conn, error) {
    if UseSocksProxy {
        socksAddr := net.JoinHostPort(SocksServer, SocksPort)
        dialer, err := proxy.SOCKS5("tcp", socksAddr, nil, proxy.Direct)
        if err != nil {
            log.Printf("SOCKS5 dialer error: %v, using direct", err)
            return net.DialTimeout(network, addr, 30*time.Second)
        }
        return dialer.Dial(network, addr)
    }
    return net.DialTimeout(network, addr, 30*time.Second)
}

// ============================================================
// Data transfer and TLS certificate generation functions
// ============================================================

// transfer: Transfer data between two connections (for tunneling)
func transfer(destination io.WriteCloser, source io.ReadCloser) {
    defer destination.Close()
    defer source.Close()
    io.Copy(destination, source)
}

// generateTLSConfig: Generate TLS configuration for HTTP/3
func generateTLSConfig() *tls.Config {
    cert, err := generateSelfSignedCert()
    if err != nil {
        log.Fatal(err)
    }
    
    return &tls.Config{
        Certificates: []tls.Certificate{*cert},
        NextProtos:   []string{"h3", "h3-29", "h3-28", "h3-27"},
        InsecureSkipVerify: true, // For testing only
    }
}

// generateSelfSignedCert: Generate self-signed certificate for testing
func generateSelfSignedCert() (*tls.Certificate, error) {
    cert, err := tls.X509KeyPair([]byte(serverCert), []byte(serverKey))
    if err != nil {
        return nil, err
    }
    return &cert, nil
}

// ============================================================
// Self-signed SSL certificates (development and testing only)
// For production, use valid certificates from a trusted CA
// ============================================================

const serverCert = `-----BEGIN CERTIFICATE-----
MIIDAzCCAeugAwIBAgIJAKqVxhqXvZCTMA0GCSqGSIb3DQEBCwUAMBgxFjAUBgNV
BAMMDWxvY2FsaG9zdDoxMjMwHhcNMjQwMTAxMDAwMDAwWhcNMjUwMTAxMDAwMDAw
WjAYMRYwFAYDVQQDDA1sb2NhbGhvc3Q6MTIzMIIBIjANBgkqhkiG9w0BAQEFAAOC
AQ8AMIIBCgKCAQEAu1zRPL5hXf8l3pFqLdZ8qHfLg1JjPkYyS3wZnZx7E5qVqNqL
-----END CERTIFICATE-----`

const serverKey = `-----BEGIN RSA PRIVATE KEY-----
MIIEogIBAAKCAQEAu1zRPL5hXf8l3pFqLdZ8qHfLg1JjPkYyS3wZnZx7E5qVqNqL
-----END RSA PRIVATE KEY-----`
