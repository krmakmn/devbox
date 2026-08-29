package mail

import (
	"encoding/base64"
	"io"
	"strings"
)

// newBase64Reader, base64 çözen bir okuyucu döner.
//
// Standart çözücü satır sonlarına takılıyor; posta gövdelerinde satırlar 76
// karakterde bölündüğü için önce onları atmak gerekiyor.
func newBase64Reader(r io.Reader) io.Reader {
	return base64.NewDecoder(base64.StdEncoding, &newlineStripper{r: r})
}

type newlineStripper struct {
	r   io.Reader
	buf []byte
}

func (n *newlineStripper) Read(p []byte) (int, error) {
	if len(n.buf) == 0 {
		tmp := make([]byte, len(p)+64)
		read, err := n.r.Read(tmp)
		if read == 0 {
			return 0, err
		}
		cleaned := strings.NewReplacer("\r", "", "\n", "", " ", "", "\t", "").
			Replace(string(tmp[:read]))
		n.buf = []byte(cleaned)
		if len(n.buf) == 0 {
			return 0, err
		}
	}
	copied := copy(p, n.buf)
	n.buf = n.buf[copied:]
	return copied, nil
}
