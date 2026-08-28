// Package dns, *.test alan adlarını 127.0.0.1'e çeviren küçük bir DNS
// sunucusu içerir.
//
// Neden hosts dosyası değil: hosts joker desteklemez. magaza.test için satır
// yazmak yetmez, admin.magaza.test için ayrı satır gerekir; dosya şişer, her
// değişiklik yükseltilmiş hak ister ve elle düzenlenmiş satırlarla karışır.
// Bir çözücü çalıştırıp Windows'un NRPT'sinde yalnız .test'i ona yönlendirmek
// hem joker sorununu çözer hem makinenin geri kalan DNS'ine (VPN, kurumsal ağ)
// dokunmaz.
//
// Bu sunucu kasten aptaldır: özyineleme yapmaz, yukarı akışa sormaz, yalnız
// kendi son eklerine cevap verir, gerisini reddeder. Açık çözücü olarak
// kötüye kullanılamaz.
package dns

import (
	"encoding/binary"
	"errors"
	"strings"
)

// Kayıt türleri ve sınıfları.
const (
	typeA     uint16 = 1
	typeCNAME uint16 = 5
	typeAAAA  uint16 = 28
	typeANY   uint16 = 255

	classIN uint16 = 1
)

// Yanıt kodları (RFC 1035 §4.1.1).
const (
	rcodeSuccess uint8 = 0
	rcodeFormErr uint8 = 1
	rcodeNotImpl uint8 = 4
	rcodeRefused uint8 = 5
)

const headerSize = 12

// namePointer, yanıttaki kayıt adının soru bölümündeki adı işaret etmesini
// sağlar: 0xC0 sıkıştırma biti + başlıktan hemen sonraki konum (12).
//
// Bu numara sayesinde ad kodlayıcısı yazmamıza hiç gerek kalmıyor; soruyu
// olduğu gibi kopyalayıp cevabı ona işaret ettiriyoruz.
var namePointer = []byte{0xC0, byte(headerSize)}

var (
	errShortMessage = errors.New("dns: ileti çok kısa")
	errBadName      = errors.New("dns: alan adı çözülemedi")
)

// query, çözümlenmiş bir DNS sorgusudur.
type query struct {
	id              uint16
	recursionWanted bool
	opcode          uint8
	name            string // sondaki nokta atılmış, küçük harf
	qtype           uint16
	qclass          uint16

	// questionRaw, soru bölümünün ham baytları. Yanıtta aynen geri
	// yazılır: yeniden kodlamaya çalışmak, kaçık ama geçerli adlarda
	// (büyük harf, kaçış dizileri) uyuşmazlık üretir.
	questionRaw []byte
}

// parseQuery, gelen iletiden ilk soruyu çözer.
func parseQuery(msg []byte) (query, error) {
	if len(msg) < headerSize {
		return query{}, errShortMessage
	}

	var q query
	q.id = binary.BigEndian.Uint16(msg[0:2])
	flags := binary.BigEndian.Uint16(msg[2:4])
	q.opcode = uint8((flags >> 11) & 0x0F)
	q.recursionWanted = flags&0x0100 != 0

	if qdcount := binary.BigEndian.Uint16(msg[4:6]); qdcount != 1 {
		// Tek sorudan fazlasını hiçbir gerçek çözücü göndermez.
		return q, errShortMessage
	}

	name, offset, err := parseName(msg, headerSize)
	if err != nil {
		return q, err
	}
	if len(msg) < offset+4 {
		return q, errShortMessage
	}
	q.name = name
	q.qtype = binary.BigEndian.Uint16(msg[offset : offset+2])
	q.qclass = binary.BigEndian.Uint16(msg[offset+2 : offset+4])
	q.questionRaw = msg[headerSize : offset+4]
	return q, nil
}

// parseName, etiket dizisini okur ve sonraki konumu döner.
//
// Sorularda ad sıkıştırması kullanılmaz; sıkıştırma işaretçisi görürsek bunu
// bozuk ileti sayıyoruz. İşaretçileri izlemek, kendi kendine döngü kuran
// kötü niyetli iletilere kapı açardı.
func parseName(msg []byte, offset int) (string, int, error) {
	var labels []string
	total := 0

	for {
		if offset >= len(msg) {
			return "", 0, errBadName
		}
		length := int(msg[offset])
		offset++

		if length == 0 {
			break
		}
		if length&0xC0 != 0 {
			return "", 0, errBadName
		}
		if length > 63 {
			return "", 0, errBadName
		}
		total += length + 1
		if total > 255 {
			return "", 0, errBadName
		}
		if offset+length > len(msg) {
			return "", 0, errBadName
		}
		labels = append(labels, string(msg[offset:offset+length]))
		offset += length
	}

	return strings.ToLower(strings.Join(labels, ".")), offset, nil
}

// response, yanıt iletisi kurar.
type response struct {
	buf     []byte
	answers int
}

func newResponse(q query, rcode uint8, authoritative bool) *response {
	r := &response{buf: make([]byte, 0, 512)}

	var flags uint16 = 0x8000 // QR: bu bir yanıt
	flags |= uint16(q.opcode) << 11
	if authoritative {
		flags |= 0x0400 // AA
	}
	if q.recursionWanted {
		flags |= 0x0100 // RD yankılanır
	}
	// RA bilerek 0: özyineleme yapmıyoruz ve yaptığımızı iddia etmiyoruz.
	flags |= uint16(rcode)

	var head [headerSize]byte
	binary.BigEndian.PutUint16(head[0:2], q.id)
	binary.BigEndian.PutUint16(head[2:4], flags)
	binary.BigEndian.PutUint16(head[4:6], 1) // QDCOUNT
	r.buf = append(r.buf, head[:]...)
	r.buf = append(r.buf, q.questionRaw...)
	return r
}

// addAddress, A ya da AAAA kaydı ekler.
func (r *response) addAddress(rrtype uint16, ttl uint32, ip []byte) {
	r.buf = append(r.buf, namePointer...)

	var rr [10]byte
	binary.BigEndian.PutUint16(rr[0:2], rrtype)
	binary.BigEndian.PutUint16(rr[2:4], classIN)
	binary.BigEndian.PutUint32(rr[4:8], ttl)
	binary.BigEndian.PutUint16(rr[8:10], uint16(len(ip)))
	r.buf = append(r.buf, rr[:]...)
	r.buf = append(r.buf, ip...)
	r.answers++
}

// bytes, ANCOUNT'u yazıp iletiyi döner.
func (r *response) bytes() []byte {
	binary.BigEndian.PutUint16(r.buf[6:8], uint16(r.answers))
	return r.buf
}
