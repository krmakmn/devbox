package mail

import (
	"bytes"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"strings"
	"time"
)

// Attachment, postaya iliştirilmiş dosya.
type Attachment struct {
	Filename    string `json:"filename"`
	ContentType string `json:"contentType"`
	Size        int    `json:"size"`

	// Data, dosyanın içeriği. JSON'a yazılmıyor; ayrı uç noktadan iniyor.
	Data []byte `json:"-"`
}

// Message, yakalanmış bir posta.
type Message struct {
	ID       string    `json:"id"`
	Received time.Time `json:"received"`

	// From ve To, SMTP zarfındaki adresler. Başlıklardaki adreslerden
	// farklı olabilir; postanın gerçekten nereye gittiğini zarf söyler.
	From string   `json:"from"`
	To   []string `json:"to"`

	Subject string `json:"subject"`

	// Header, çözümlenmiş başlıklar.
	Header map[string][]string `json:"headers"`

	// Text ve HTML, gövdenin iki biçimi. İkisi de boş olabilir.
	Text string `json:"text"`
	HTML string `json:"html"`

	Attachments []Attachment `json:"attachments"`

	// Raw, postanın ham hâli. Bir şey yanlış çözümlendiyse tek doğru
	// kaynak bu; arayüzden görülebiliyor.
	Raw []byte `json:"-"`

	Size int `json:"size"`

	// Relay, posta gerçekten gönderildiyse sonucu. Store kilidi altında
	// yazılıyor; doğrudan okumak yerine Store.Get kullanılmalı.
	Relay *RelayResult `json:"relay,omitempty"`
}

// Summary, listede gösterilecek özet.
type Summary struct {
	// Relayed, postanın gerçekten gönderilip gönderilmediği.
	Relayed bool `json:"relayed,omitempty"`

	ID              string    `json:"id"`
	Received        time.Time `json:"received"`
	From            string    `json:"from"`
	To              []string  `json:"to"`
	Subject         string    `json:"subject"`
	Size            int       `json:"size"`
	AttachmentCount int       `json:"attachmentCount"`
	HasHTML         bool      `json:"hasHtml"`
}

// Summary, postanın özetini döner.
func (m *Message) Summary() Summary {
	return Summary{
		ID: m.ID, Received: m.Received, From: m.From, To: m.To,
		Subject: m.Subject, Size: m.Size,
		AttachmentCount: len(m.Attachments), HasHTML: m.HTML != "",
		Relayed: m.Relay != nil && m.Relay.Error == "" && len(m.Relay.Recipients) > 0,
	}
}

// Parse, ham postayı çözümler.
//
// Çözümleme asla hata döndürmüyor: bozuk bir posta da yakalanmalı ve
// arayüzde görünmeli. Çözümlenemeyen kısımlar boş kalıyor, ham hâl her
// zaman elde tutuluyor — kullanıcı "neden böyle göründü" diye sorduğunda
// bakacak bir yer olsun.
func Parse(raw []byte, from string, to []string) *Message {
	msg := &Message{
		ID:       newID(),
		Received: time.Now(),
		From:     from,
		To:       to,
		Raw:      raw,
		Size:     len(raw),
		Header:   map[string][]string{},
	}

	parsed, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		// Başlıklar çözümlenemedi: gövdeyi düz metin sayıyoruz.
		msg.Text = string(raw)
		return msg
	}

	for k, v := range parsed.Header {
		msg.Header[k] = v
	}
	msg.Subject = decodeHeader(parsed.Header.Get("Subject"))

	// Zarfta adres yoksa başlıklara düşüyoruz: bazı istemciler MAIL FROM'u
	// boş bırakıyor (bounce adresi).
	if msg.From == "" {
		msg.From = decodeHeader(parsed.Header.Get("From"))
	}
	if len(msg.To) == 0 {
		if v := parsed.Header.Get("To"); v != "" {
			msg.To = splitAddresses(decodeHeader(v))
		}
	}

	parsePart(msg,
		parsed.Header.Get("Content-Type"),
		parsed.Header.Get("Content-Transfer-Encoding"),
		parsed.Header.Get("Content-Disposition"),
		parsed.Body)
	return msg
}

// parsePart, bir gövde parçasını çözümler; çok parçalıysa özyineler.
func parsePart(msg *Message, contentType, encoding, disposition string, body io.Reader) {
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		mediaType = "text/plain"
	}

	if strings.HasPrefix(mediaType, "multipart/") {
		boundary := params["boundary"]
		if boundary == "" {
			return
		}
		mr := multipart.NewReader(body, boundary)
		for {
			part, err := mr.NextPart()
			if err != nil {
				return
			}
			parsePart(msg,
				part.Header.Get("Content-Type"),
				part.Header.Get("Content-Transfer-Encoding"),
				part.Header.Get("Content-Disposition"),
				part)
			part.Close()
		}
	}

	data, err := io.ReadAll(io.LimitReader(decodeBody(body, encoding), maxMessageSize))
	if err != nil {
		return
	}

	// Dosya adı varsa ek; yoksa gövde. Ekler çoğunlukla adlarını
	// Content-Disposition'da taşıyor, Content-Type'ta değil.
	filename := decodeHeader(attachmentName(params, disposition))
	isAttachment := filename != "" || strings.HasPrefix(strings.ToLower(disposition), "attachment")

	switch {
	case isAttachment:
		if filename == "" {
			filename = "adsiz-ek"
		}
		msg.Attachments = append(msg.Attachments, Attachment{
			Filename: filename, ContentType: mediaType, Size: len(data), Data: data,
		})
	case mediaType == "text/html":
		msg.HTML += string(data)
	case strings.HasPrefix(mediaType, "text/"):
		msg.Text += string(data)
	default:
		// Metin olmayan, adı da olmayan parça: yine de ek sayıyoruz ki
		// kaybolmasın.
		msg.Attachments = append(msg.Attachments, Attachment{
			Filename: "ek-" + mediaType, ContentType: mediaType, Size: len(data), Data: data,
		})
	}
}

// attachmentName, ekin dosya adını bulur.
//
// Önce Content-Disposition'ın filename parametresine bakıyoruz (yaygın
// olan), sonra Content-Type'ın name parametresine (eski istemciler).
func attachmentName(contentTypeParams map[string]string, disposition string) string {
	if disposition != "" {
		if _, params, err := mime.ParseMediaType(disposition); err == nil {
			if name := params["filename"]; name != "" {
				return name
			}
		}
	}
	return contentTypeParams["name"]
}

// decodeBody, aktarım kodlamasını çözer.
func decodeBody(body io.Reader, encoding string) io.Reader {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "quoted-printable":
		return quotedprintable.NewReader(body)
	case "base64":
		return newBase64Reader(body)
	default:
		return body
	}
}

// decodeHeader, RFC 2047 ile kodlanmış başlıkları çözer ("=?UTF-8?B?...?=").
//
// Türkçe konu satırları neredeyse her zaman böyle geliyor; çözmezsek arayüzde
// okunmaz bir dizi görünüyor.
func decodeHeader(value string) string {
	decoder := &mime.WordDecoder{}
	decoded, err := decoder.DecodeHeader(value)
	if err != nil {
		return value
	}
	return decoded
}

// splitAddresses, "a@b, c@d" biçimini ayırır.
func splitAddresses(value string) []string {
	list, err := mail.ParseAddressList(value)
	if err != nil {
		var out []string
		for _, part := range strings.Split(value, ",") {
			if p := strings.TrimSpace(part); p != "" {
				out = append(out, p)
			}
		}
		return out
	}
	out := make([]string, 0, len(list))
	for _, addr := range list {
		out = append(out, addr.Address)
	}
	return out
}
