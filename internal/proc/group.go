// Package proc, DevBox'ın başlattığı yardımcı süreçlerin (php-cgi, mysqld,
// httpd...) hiçbir koşulda arkada kalmamasını sağlar.
//
// Laragon'un en can sıkıcı davranışlarından biri budur: arayüz çökerse ya da
// zorla kapatılırsa php-cgi.exe ve mysqld.exe ayakta kalır, portları tutar,
// bir sonraki başlatmayı engeller. Çözüm işletim sistemine göre değişiyor:
//
//   - Windows: bir İş Nesnesi (Job Object) oluşturup tüm çocukları ona
//     atıyoruz. JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE bayrağı sayesinde iş
//     nesnesinin tutamağı kapanınca çekirdek bütün üyeleri öldürür — DevBox
//     çökse, öldürülse, hatta hata ayıklayıcı altında ölse bile.
//   - Unix: çocukları kendi süreç grubuna alıp gruba sinyal gönderiyoruz.
//     (Geliştirme ve CI Linux'ta da koştuğu için gerekli.)
package proc

import (
	"os/exec"
)

// Group, birlikte yaşayıp birlikte ölen bir süreç kümesidir.
//
// Kullanım: NewGroup, ardından her komut için Prepare (Start'tan önce) ve
// Add (Start'tan sonra), en sonda Close.
type Group struct {
	impl *groupImpl
}

// NewGroup yeni bir süreç grubu oluşturur.
func NewGroup() (*Group, error) {
	impl, err := newGroupImpl()
	if err != nil {
		return nil, err
	}
	return &Group{impl: impl}, nil
}

// Prepare, komutu gruba katılabilecek biçimde ayarlar. Start'tan önce
// çağrılmalıdır.
func (g *Group) Prepare(cmd *exec.Cmd) {
	g.impl.prepare(cmd)
}

// Add, başlatılmış bir süreci gruba katar. Start'tan hemen sonra
// çağrılmalıdır.
func (g *Group) Add(cmd *exec.Cmd) error {
	return g.impl.add(cmd)
}

// Close, gruptaki tüm süreçleri sonlandırır ve kaynakları bırakır.
// Birden çok kez çağrılabilir.
func (g *Group) Close() error {
	return g.impl.close()
}
