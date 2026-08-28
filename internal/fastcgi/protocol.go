// Package fastcgi, FastCGI 1.0 protokolünün istemci tarafını uygular.
//
// Windows'ta PHP-FPM yok; elimizde tek istek görüp ölen php-cgi.exe var.
// DevBox bu yüzden php-cgi süreçlerini kendisi havuzlar ve onlarla FastCGI
// konuşur. Bu paket o konuşmanın alt katmanı: kayıt (record) çerçeveleme ve
// isim-değer çifti kodlaması.
//
// Referans: FastCGI Specification 1.0, bölüm 3.
package fastcgi

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sort"
)

const version1 = 1

// Kayıt türleri (spec 3.3).
const (
	typeBeginRequest    uint8 = 1
	typeAbortRequest    uint8 = 2
	typeEndRequest      uint8 = 3
	typeParams          uint8 = 4
	typeStdin           uint8 = 5
	typeStdout          uint8 = 6
	typeStderr          uint8 = 7
	typeData            uint8 = 8
	typeGetValues       uint8 = 9
	typeGetValuesResult uint8 = 10
	typeUnknownType     uint8 = 11
)

// Roller (spec 6). Bize yalnız Responder lazım.
const roleResponder uint16 = 1

// BeginRequest bayrakları.
const flagKeepConn uint8 = 1

// END_REQUEST içindeki protokol durumları.
const (
	statusRequestComplete uint8 = 0
	statusCantMpxConn     uint8 = 1
	statusOverloaded      uint8 = 2
	statusUnknownRole     uint8 = 3
)

// Bir kaydın gövdesi contentLength alanına sığmalı: en fazla 64 KiB - 1.
// PARAMS ve STDIN akışlarını bu boyutta parçalara bölüyoruz.
const maxRecordBody = 65535

// headerSize, her kaydın başındaki sabit başlık uzunluğu.
const headerSize = 8

// requestID: tek bir bağlantı üzerinde tek istek çalıştırdığımız için sabit.
// Çoğullama (multiplexing) php-cgi tarafından zaten desteklenmiyor.
const requestID uint16 = 1

var errRecordTooLarge = errors.New("fastcgi: kayıt gövdesi 65535 baytı aşıyor")

// writeRecord tek bir FastCGI kaydı yazar. Gövde 8 baytın katına
// tamamlanır; spec bunu zorunlu tutmaz ama tavsiye eder ve bazı
// uygulamalar hizalanmış kayıtlarda gözle görülür biçimde hızlıdır.
func writeRecord(w io.Writer, typ uint8, body []byte) error {
	if len(body) > maxRecordBody {
		return errRecordTooLarge
	}
	padding := (8 - len(body)%8) % 8

	var buf [headerSize]byte
	buf[0] = version1
	buf[1] = typ
	binary.BigEndian.PutUint16(buf[2:4], requestID)
	binary.BigEndian.PutUint16(buf[4:6], uint16(len(body)))
	buf[6] = uint8(padding)
	buf[7] = 0

	if _, err := w.Write(buf[:]); err != nil {
		return err
	}
	if len(body) > 0 {
		if _, err := w.Write(body); err != nil {
			return err
		}
	}
	if padding > 0 {
		var pad [8]byte
		if _, err := w.Write(pad[:padding]); err != nil {
			return err
		}
	}
	return nil
}

// record, okunmuş tek bir kaydı temsil eder.
type record struct {
	Type      uint8
	RequestID uint16
	Body      []byte
}

// readRecord bir kaydı okur ve dolgu baytlarını atar.
func readRecord(r io.Reader) (record, error) {
	var head [headerSize]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return record{}, err
	}
	if head[0] != version1 {
		return record{}, fmt.Errorf("fastcgi: beklenmeyen protokol sürümü %d", head[0])
	}

	rec := record{
		Type:      head[1],
		RequestID: binary.BigEndian.Uint16(head[2:4]),
	}
	contentLen := binary.BigEndian.Uint16(head[4:6])
	padLen := head[6]

	if contentLen > 0 {
		rec.Body = make([]byte, contentLen)
		if _, err := io.ReadFull(r, rec.Body); err != nil {
			return record{}, err
		}
	}
	if padLen > 0 {
		if _, err := io.CopyN(io.Discard, r, int64(padLen)); err != nil {
			return record{}, err
		}
	}
	return rec, nil
}

// beginRequestBody, BEGIN_REQUEST kaydının 8 baytlık gövdesini üretir.
func beginRequestBody(role uint16, flags uint8) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint16(b[0:2], role)
	b[2] = flags
	return b
}

// parseEndRequest, END_REQUEST gövdesini çözer.
func parseEndRequest(body []byte) (appStatus uint32, protocolStatus uint8, err error) {
	if len(body) < 8 {
		return 0, 0, errors.New("fastcgi: END_REQUEST gövdesi kısa")
	}
	return binary.BigEndian.Uint32(body[0:4]), body[4], nil
}

// appendLength, isim-değer çiftlerinde kullanılan değişken uzunluk
// kodlamasını yazar: 127'ye kadar tek bayt, üstü 4 bayt ve en yüksek bit 1.
func appendLength(dst []byte, n int) []byte {
	if n < 128 {
		return append(dst, byte(n))
	}
	return append(dst,
		byte(n>>24)|0x80,
		byte(n>>16),
		byte(n>>8),
		byte(n),
	)
}

// encodeParams, ortam değişkenlerini PARAMS akışının gövdesine çevirir.
// Anahtarlar sıralanır: üretilen bayt dizisinin belirlenimci olması
// testleri ve günlük karşılaştırmasını kolaylaştırır.
func encodeParams(params map[string]string) []byte {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var out []byte
	for _, k := range keys {
		v := params[k]
		out = appendLength(out, len(k))
		out = appendLength(out, len(v))
		out = append(out, k...)
		out = append(out, v...)
	}
	return out
}

// decodeParams, encodeParams'ın tersi. GET_VALUES_RESULT'ı çözmek ve
// testlerde gidiş-dönüş doğrulaması yapmak için.
func decodeParams(b []byte) (map[string]string, error) {
	out := make(map[string]string)
	for len(b) > 0 {
		nameLen, n, err := readLength(b)
		if err != nil {
			return nil, err
		}
		b = b[n:]
		valLen, n, err := readLength(b)
		if err != nil {
			return nil, err
		}
		b = b[n:]
		if len(b) < nameLen+valLen {
			return nil, errors.New("fastcgi: isim-değer çifti kesik")
		}
		out[string(b[:nameLen])] = string(b[nameLen : nameLen+valLen])
		b = b[nameLen+valLen:]
	}
	return out, nil
}

func readLength(b []byte) (length, consumed int, err error) {
	if len(b) == 0 {
		return 0, 0, errors.New("fastcgi: uzunluk alanı eksik")
	}
	if b[0]>>7 == 0 {
		return int(b[0]), 1, nil
	}
	if len(b) < 4 {
		return 0, 0, errors.New("fastcgi: 4 baytlık uzunluk alanı kesik")
	}
	n := int(binary.BigEndian.Uint32(b[0:4]) & 0x7fffffff)
	return n, 4, nil
}

// writeStream, bir akışı (PARAMS/STDIN) 64 KiB'lık kayıtlara bölerek yazar
// ve sonunda akışın bittiğini bildiren boş kaydı gönderir.
func writeStream(w io.Writer, typ uint8, data []byte) error {
	for len(data) > 0 {
		n := len(data)
		if n > maxRecordBody {
			n = maxRecordBody
		}
		if err := writeRecord(w, typ, data[:n]); err != nil {
			return err
		}
		data = data[n:]
	}
	// Boş kayıt = akış sonu.
	return writeRecord(w, typ, nil)
}

// copyStream, io.Reader'ı doğrudan kayıtlara bölerek aktarır. Büyük
// yüklemelerde tüm gövdeyi belleğe almamak için writeStream yerine bu
// kullanılır.
func copyStream(w io.Writer, typ uint8, r io.Reader) error {
	if r == nil {
		return writeRecord(w, typ, nil)
	}
	buf := make([]byte, maxRecordBody)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			if werr := writeRecord(w, typ, buf[:n]); werr != nil {
				return werr
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
	}
	return writeRecord(w, typ, nil)
}
