package dns

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"
)

// DefaultAddr, çözücünün varsayılan adresi. Gerekçesi Config.Addr'de.
const DefaultAddr = "127.0.0.53:53"

// Varsayılanlar.
const (
	defaultTTL     uint32 = 10
	maxUDPMessage         = 512
	maxTCPMessage         = 8 * 1024
	tcpIdleTimeout        = 5 * time.Second
)

// Config, çözücünün ayarları.
type Config struct {
	// Addr, dinlenecek adres. Boşsa DefaultAddr.
	//
	// Port 53 olmak zorunda: NRPT kuralı yalnız bir sunucu IP'si alır,
	// port taşıyamaz. Windows'ta 1024 altındaki portlar ayrıcalıklı
	// olmadığı için bu yönetici hakkı gerektirmiyor (Unix'in aksine).
	//
	// Varsayılan IP 127.0.0.1 değil 127.0.0.53: 127/8'in tamamı loopback'e
	// bağlı olduğu için ayrı bir adres kullanmak, 127.0.0.1:53'ü tutan
	// başka bir şeyle (Docker Desktop'ın vpnkit'i, ICS, kurumsal DNS
	// ajanları) çakışmayı baştan önlüyor.
	Addr string

	// Suffixes, cevap verilecek son ekler ("test" → *.test ve test).
	Suffixes []string

	// IPv4 / IPv6, döndürülecek adresler. Boşsa loopback.
	IPv4 net.IP
	IPv6 net.IP

	// TTL, yanıtların yaşam süresi. Kısa tutuluyor: proje kaldırıldığında
	// çözümlemenin uzun süre önbellekte kalmasını istemiyoruz.
	TTL uint32

	Logger *slog.Logger
}

func (c *Config) applyDefaults() {
	if c.Addr == "" {
		c.Addr = DefaultAddr
	}
	if c.IPv4 == nil {
		c.IPv4 = net.IPv4(127, 0, 0, 1)
	}
	if c.IPv6 == nil {
		c.IPv6 = net.ParseIP("::1")
	}
	if c.TTL == 0 {
		c.TTL = defaultTTL
	}
	for i, s := range c.Suffixes {
		c.Suffixes[i] = strings.ToLower(strings.Trim(s, "."))
	}
}

// Server, UDP ve TCP üzerinden hizmet veren çözücüdür.
type Server struct {
	cfg Config

	mu   sync.Mutex
	udp  *net.UDPConn
	tcp  *net.TCPListener
	done bool
	wg   sync.WaitGroup
}

// New, sunucuyu kurar ama dinlemeye başlamaz.
func New(cfg Config) *Server {
	cfg.applyDefaults()
	return &Server{cfg: cfg}
}

// Start, UDP ve TCP dinleyicilerini açar ve arka planda hizmet vermeye başlar.
//
// DNS hem UDP hem TCP ister: yanıt 512 baytı aşarsa ya da çözücü öyle
// tercih ederse istemci TCP'ye düşer. Yalnız UDP dinlemek, nadiren ve
// açıklanması zor biçimde bozulan bir kurulum demek.
func (s *Server) Start() error {
	udpAddr, err := net.ResolveUDPAddr("udp", s.cfg.Addr)
	if err != nil {
		return fmt.Errorf("dns: adres çözülemedi: %w", err)
	}
	udp, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return fmt.Errorf("dns: UDP dinlenemedi: %w", err)
	}

	// UDP portu 0 ile açıldıysa TCP'yi de aynı porta bağla.
	tcpAddr := &net.TCPAddr{IP: udpAddr.IP, Port: udp.LocalAddr().(*net.UDPAddr).Port}
	tcp, err := net.ListenTCP("tcp", tcpAddr)
	if err != nil {
		udp.Close()
		return fmt.Errorf("dns: TCP dinlenemedi: %w", err)
	}

	s.mu.Lock()
	s.udp, s.tcp = udp, tcp
	s.mu.Unlock()

	s.wg.Add(2)
	go s.serveUDP(udp)
	go s.serveTCP(tcp)
	return nil
}

// Addr, gerçekten bağlanılan adres (port 0 verildiyse çözülmüş hâli).
func (s *Server) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.udp == nil {
		return s.cfg.Addr
	}
	return s.udp.LocalAddr().String()
}

// Close, dinleyicileri kapatır.
func (s *Server) Close() error {
	s.mu.Lock()
	if s.done {
		s.mu.Unlock()
		return nil
	}
	s.done = true
	udp, tcp := s.udp, s.tcp
	s.mu.Unlock()

	if udp != nil {
		udp.Close()
	}
	if tcp != nil {
		tcp.Close()
	}
	s.wg.Wait()
	return nil
}

func (s *Server) closed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.done
}

func (s *Server) serveUDP(conn *net.UDPConn) {
	defer s.wg.Done()
	buf := make([]byte, maxUDPMessage)
	for {
		n, addr, err := conn.ReadFromUDP(buf)
		if err != nil {
			if s.closed() {
				return
			}
			s.logf("UDP okuma hatası: %v", err)
			continue
		}
		reply := s.Respond(buf[:n])
		if reply == nil {
			continue
		}
		if _, err := conn.WriteToUDP(reply, addr); err != nil && !s.closed() {
			s.logf("UDP yazma hatası: %v", err)
		}
	}
}

func (s *Server) serveTCP(ln *net.TCPListener) {
	defer s.wg.Done()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if s.closed() {
				return
			}
			s.logf("TCP kabul hatası: %v", err)
			continue
		}
		go s.handleTCP(conn)
	}
}

// handleTCP, TCP üzerindeki iletileri işler. TCP'de her iletinin başında
// 2 baytlık uzunluk alanı vardır (RFC 1035 §4.2.2).
func (s *Server) handleTCP(conn net.Conn) {
	defer conn.Close()
	for {
		conn.SetDeadline(time.Now().Add(tcpIdleTimeout))

		var lenBuf [2]byte
		if _, err := io.ReadFull(conn, lenBuf[:]); err != nil {
			return
		}
		length := int(binary.BigEndian.Uint16(lenBuf[:]))
		if length == 0 || length > maxTCPMessage {
			return
		}
		msg := make([]byte, length)
		if _, err := io.ReadFull(conn, msg); err != nil {
			return
		}

		reply := s.Respond(msg)
		if reply == nil {
			return
		}
		binary.BigEndian.PutUint16(lenBuf[:], uint16(len(reply)))
		if _, err := conn.Write(append(lenBuf[:], reply...)); err != nil {
			return
		}
	}
}

// Respond, bir sorgu iletisine yanıt üretir. Yanıt verilemeyecek kadar bozuk
// iletiler için nil döner.
//
// Taşımadan bağımsız olduğu için doğrudan test edilebilir.
func (s *Server) Respond(msg []byte) []byte {
	q, err := parseQuery(msg)
	if err != nil {
		if errors.Is(err, errShortMessage) && len(msg) >= headerSize {
			// Kimliği okuyabiliyorsak biçim hatası bildirelim; sessiz
			// kalmak istemciyi zaman aşımına kadar bekletir.
			return newResponse(query{id: binary.BigEndian.Uint16(msg[0:2])}, rcodeFormErr, false).bytes()
		}
		return nil
	}

	// Yalnız standart sorgu (QUERY). Ters çözümleme, güncelleme, bildirim
	// bizim işimiz değil.
	if q.opcode != 0 {
		return newResponse(q, rcodeNotImpl, false).bytes()
	}
	if q.qclass != classIN {
		return newResponse(q, rcodeRefused, false).bytes()
	}
	if !s.owns(q.name) {
		// Bizim son eklerimiz değil: reddet. Yukarı akışa sormak bizi açık
		// çözücüye dönüştürürdü.
		return newResponse(q, rcodeRefused, false).bytes()
	}

	r := newResponse(q, rcodeSuccess, true)
	switch q.qtype {
	case typeA, typeANY:
		if ip := s.cfg.IPv4.To4(); ip != nil {
			r.addAddress(typeA, s.cfg.TTL, ip)
		}
		if q.qtype == typeANY {
			if ip := s.cfg.IPv6.To16(); ip != nil {
				r.addAddress(typeAAAA, s.cfg.TTL, ip)
			}
		}
	case typeAAAA:
		if ip := s.cfg.IPv6.To16(); ip != nil {
			r.addAddress(typeAAAA, s.cfg.TTL, ip)
		}
	default:
		// Ad bizim ama bu tür için kaydımız yok: NOERROR + boş yanıt
		// (NODATA). NXDOMAIN döndürmek yanlış olurdu; ad var.
	}
	return r.bytes()
}

// owns, adın bizim son eklerimizden birine ait olup olmadığını söyler.
func (s *Server) owns(name string) bool {
	name = strings.ToLower(strings.TrimSuffix(name, "."))
	for _, suffix := range s.cfg.Suffixes {
		if suffix == "" {
			continue
		}
		if name == suffix || strings.HasSuffix(name, "."+suffix) {
			return true
		}
	}
	return false
}

func (s *Server) logf(format string, args ...any) {
	if s.cfg.Logger == nil {
		return
	}
	s.cfg.Logger.Warn(fmt.Sprintf(format, args...), "bileşen", "dns")
}

// Resolver, sunucuya doğrudan soran bir net.Resolver döner. Testler ve
// tanılama için.
func (s *Server) Resolver() *net.Resolver {
	addr := s.Addr()
	return &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		},
	}
}
