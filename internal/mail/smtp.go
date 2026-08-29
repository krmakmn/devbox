// Package mail, giden postayı yakalayan yerel bir SMTP sunucusu ve onu
// okumak için bir HTTP arayüzü sağlar.
//
// # Neden Mailpit değil
//
// Yol haritası hazır bir araç (Mailpit) çalıştırmayı öngörüyordu. İki sebeple
// kendi yakalayıcımızı yazdık:
//
//   - Manifest yayın altyapısı henüz yok, dolayısıyla ikiliyi indirip
//     doğrulayarak kurmanın yolu da yok. Kullanıcıdan elle indirmesini
//     istemek, DevBox'ın çözmeye çalıştığı sürtünmenin ta kendisi.
//   - Yakalayıcının ihtiyaç duyduğu SMTP alt kümesi küçük ve iyi tanımlı:
//     postayı kabul et, asla iletme. TLS pazarlığı, kuyruk, yeniden deneme,
//     bounce yok. Yanlış davranış da sessizce değil gürültüyle belli oluyor
//     (posta gelmiyor).
//
// Mailpit'in arama ve etiketleme gibi özellikleri burada yok; onu tercih
// edenler runtime kayıt defteriyle kurabilecek.
//
// # Kimlik doğrulama neden kabul ediliyor
//
// Sunucu AUTH PLAIN ve AUTH LOGIN'i duyuruyor ve **her kimlik bilgisini
// kabul ediyor**. Sebebi pratik: Laravel, Symfony ve WordPress eklentileri
// çoğunlukla kullanıcı adı/parola ile yapılandırılmış geliyor ve sunucu
// AUTH'u desteklemezse bağlantıyı hata sayıyorlar. Yalnız loopback'i
// dinlediğimiz için burada doğrulanacak bir şey de yok.
package mail

import (
	"bufio"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"
)

// DefaultSMTPAddr, yakalayıcının varsayılan adresi.
//
// 1025, yerel posta yakalayıcılarının yerleşik portu (MailHog ve Mailpit de
// kullanıyor); 25 numaralı port çoğu makinede kısıtlı ve gerçek posta
// sunucularıyla karışıyor.
const DefaultSMTPAddr = "127.0.0.1:1025"

const (
	// maxMessageSize, tek bir postanın üst sınırı.
	maxMessageSize = 25 << 20

	// maxLineLength, SMTP satır sınırı (RFC 5321 §4.5.3.1'in üstünde,
	// hoşgörülü davranıyoruz).
	maxLineLength = 64 << 10

	// commandTimeout, istemcinin bir komut göndermesi için beklenen süre.
	commandTimeout = 5 * time.Minute
)

// SMTPServer, postayı yakalayan SMTP sunucusu.
type SMTPServer struct {
	// Addr, dinlenecek adres. Boşsa DefaultSMTPAddr.
	//
	// Yalnız loopback: kimlik doğrulaması olmayan, her postayı kabul eden
	// bir sunucuyu ağa açmak açık röle demek.
	Addr string

	// Store, yakalanan postaların konacağı yer.
	Store *Store

	// Hostname, karşılama satırında görünen ad.
	Hostname string

	Logger *slog.Logger

	mu   sync.Mutex
	ln   net.Listener
	done bool
	wg   sync.WaitGroup
}

// Start, dinlemeye başlar.
func (s *SMTPServer) Start() error {
	addr := s.Addr
	if addr == "" {
		addr = DefaultSMTPAddr
	}
	if s.Hostname == "" {
		s.Hostname = "devbox"
	}
	if s.Store == nil {
		s.Store = NewStore(0)
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("mail: SMTP dinlenemedi: %w", err)
	}

	s.mu.Lock()
	s.ln = ln
	s.mu.Unlock()

	s.wg.Add(1)
	go s.serve(ln)
	return nil
}

// Addr olarak gerçekten bağlanılan adresi döner (port 0 verildiyse çözülmüş).
func (s *SMTPServer) ListenAddr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ln == nil {
		return s.Addr
	}
	return s.ln.Addr().String()
}

// Close, sunucuyu kapatır.
func (s *SMTPServer) Close() error {
	s.mu.Lock()
	if s.done {
		s.mu.Unlock()
		return nil
	}
	s.done = true
	ln := s.ln
	s.mu.Unlock()

	if ln != nil {
		ln.Close()
	}
	s.wg.Wait()
	return nil
}

func (s *SMTPServer) closed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.done
}

func (s *SMTPServer) serve(ln net.Listener) {
	defer s.wg.Done()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if s.closed() {
				return
			}
			s.logf("bağlantı kabul edilemedi: %v", err)
			continue
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.handle(conn)
		}()
	}
}

// session, tek bir SMTP oturumunun durumu.
type session struct {
	from string
	to   []string
}

func (s *SMTPServer) handle(conn net.Conn) {
	defer conn.Close()

	r := bufio.NewReaderSize(conn, 8192)
	w := bufio.NewWriter(conn)

	reply := func(format string, args ...any) error {
		conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
		if _, err := fmt.Fprintf(w, format+"\r\n", args...); err != nil {
			return err
		}
		return w.Flush()
	}

	if err := reply("220 %s DevBox posta yakalayıcı", s.Hostname); err != nil {
		return
	}

	var sess session
	for {
		conn.SetReadDeadline(time.Now().Add(commandTimeout))
		line, err := readLine(r)
		if err != nil {
			return
		}

		command, arg := splitCommand(line)
		switch command {
		case "HELO":
			sess = session{}
			if reply("250 %s", s.Hostname) != nil {
				return
			}

		case "EHLO":
			sess = session{}
			// Yetenekler: boyut sınırı, 8 bit gövde ve kimlik doğrulama.
			lines := []string{
				fmt.Sprintf("250-%s", s.Hostname),
				fmt.Sprintf("250-SIZE %d", maxMessageSize),
				"250-8BITMIME",
				"250-AUTH PLAIN LOGIN",
				"250 HELP",
			}
			conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
			if _, err := w.WriteString(strings.Join(lines, "\r\n") + "\r\n"); err != nil {
				return
			}
			if w.Flush() != nil {
				return
			}

		case "AUTH":
			// Her kimlik bilgisi kabul ediliyor; gerekçesi paket
			// açıklamasında.
			if strings.HasPrefix(strings.ToUpper(arg), "LOGIN") {
				// İstemci kullanıcı adı ve parolayı ayrı satırlarda
				// bekliyor.
				if reply("334 %s", base64.StdEncoding.EncodeToString([]byte("Username:"))) != nil {
					return
				}
				if _, err := readLine(r); err != nil {
					return
				}
				if reply("334 %s", base64.StdEncoding.EncodeToString([]byte("Password:"))) != nil {
					return
				}
				if _, err := readLine(r); err != nil {
					return
				}
			} else if strings.ToUpper(arg) == "PLAIN" {
				// Kimlik bilgisi ayrı satırda geliyor.
				if reply("334 ") != nil {
					return
				}
				if _, err := readLine(r); err != nil {
					return
				}
			}
			if reply("235 2.7.0 Kimlik kabul edildi") != nil {
				return
			}

		case "MAIL":
			sess.from = extractAddress(arg)
			sess.to = nil
			if reply("250 2.1.0 Gönderen kabul edildi") != nil {
				return
			}

		case "RCPT":
			addr := extractAddress(arg)
			if addr == "" {
				if reply("501 5.5.4 Alıcı çözümlenemedi") != nil {
					return
				}
				continue
			}
			sess.to = append(sess.to, addr)
			if reply("250 2.1.5 Alıcı kabul edildi") != nil {
				return
			}

		case "DATA":
			if sess.from == "" && len(sess.to) == 0 {
				if reply("503 5.5.1 Önce MAIL FROM gerekli") != nil {
					return
				}
				continue
			}
			if reply("354 Veriyi girin; <CRLF>.<CRLF> ile bitirin") != nil {
				return
			}

			conn.SetReadDeadline(time.Now().Add(commandTimeout))
			raw, err := readData(r)
			if err != nil {
				if errors.Is(err, errTooLarge) {
					reply("552 5.3.4 Posta çok büyük")
					continue
				}
				return
			}

			msg := Parse(raw, sess.from, sess.to)
			s.Store.Add(msg)
			s.logf("posta yakalandı: %s → %s (%s)", msg.From, strings.Join(msg.To, ", "), msg.Subject)

			sess = session{}
			if reply("250 2.0.0 Alındı: %s", msg.ID) != nil {
				return
			}

		case "RSET":
			sess = session{}
			if reply("250 2.0.0 Sıfırlandı") != nil {
				return
			}

		case "NOOP":
			if reply("250 2.0.0 Tamam") != nil {
				return
			}

		case "VRFY":
			// Yakalayıcı her adresi kabul ediyor; doğrulama anlamsız.
			if reply("252 2.5.2 Doğrulanamaz ama denenebilir") != nil {
				return
			}

		case "QUIT":
			reply("221 2.0.0 Hoşça kalın")
			return

		default:
			if reply("500 5.5.2 Anlaşılmayan komut") != nil {
				return
			}
		}
	}
}

var errTooLarge = errors.New("mail: posta çok büyük")

// readLine, CRLF ile biten bir satır okur.
func readLine(r *bufio.Reader) (string, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return "", err
	}
	if len(line) > maxLineLength {
		return "", errors.New("mail: satır çok uzun")
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// readData, DATA gövdesini "\r\n.\r\n" görene kadar okur.
//
// Nokta doldurma (dot stuffing) geri alınıyor: satır başındaki tek nokta,
// protokolün sonlandırıcıyla karışmaması için istemci tarafından
// ikilenmiştir (RFC 5321 §4.5.2).
func readData(r *bufio.Reader) ([]byte, error) {
	var out []byte
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		trimmed := strings.TrimRight(line, "\r\n")
		if trimmed == "." {
			return out, nil
		}
		if strings.HasPrefix(trimmed, "..") {
			trimmed = trimmed[1:]
		}
		out = append(out, trimmed...)
		out = append(out, '\r', '\n')

		if len(out) > maxMessageSize {
			// Kalanı yutup bağlantıyı ayakta tutuyoruz ki istemci
			// anlamlı bir hata görsün.
			if err := discardUntilDot(r); err != nil {
				return nil, err
			}
			return nil, errTooLarge
		}
	}
}

func discardUntilDot(r *bufio.Reader) error {
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return err
		}
		if strings.TrimRight(line, "\r\n") == "." {
			return nil
		}
	}
}

// splitCommand, satırı komut ve argümanına ayırır.
func splitCommand(line string) (command, arg string) {
	line = strings.TrimSpace(line)
	if i := strings.IndexAny(line, " :"); i >= 0 {
		return strings.ToUpper(line[:i]), strings.TrimSpace(line[i+1:])
	}
	return strings.ToUpper(line), ""
}

// extractAddress, "FROM:<a@b> SIZE=123" gibi bir argümandan adresi çıkarır.
func extractAddress(arg string) string {
	if i := strings.Index(arg, "<"); i >= 0 {
		if j := strings.Index(arg[i:], ">"); j > 0 {
			return arg[i+1 : i+j]
		}
	}
	// Tırnaksız biçim: "FROM: a@b"
	fields := strings.Fields(strings.TrimPrefix(strings.TrimPrefix(arg, "FROM:"), "TO:"))
	if len(fields) > 0 && strings.Contains(fields[0], "@") {
		return strings.Trim(fields[0], "<>")
	}
	return ""
}

// newID, posta için rastgele bir kimlik üretir.
func newID() string {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(raw[:])
}

func (s *SMTPServer) logf(format string, args ...any) {
	if s.Logger == nil {
		return
	}
	s.Logger.Info(fmt.Sprintf(format, args...), "bileşen", "mail")
}

var _ io.Reader = (*bufio.Reader)(nil)
