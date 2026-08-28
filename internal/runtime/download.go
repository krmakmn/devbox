package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Progress, indirme ilerlemesini bildirir.
type Progress struct {
	Downloaded int64
	Total      int64 // bilinmiyorsa 0
}

// Percent, yüzde olarak ilerleme. Toplam bilinmiyorsa -1.
func (p Progress) Percent() float64 {
	if p.Total <= 0 {
		return -1
	}
	return float64(p.Downloaded) / float64(p.Total) * 100
}

// DefaultClient, indirmelerde kullanılan istemci.
//
// Toplam bir zaman aşımı yok: 300 MB'lık bir PHP arşivi yavaş bir bağlantıda
// dakikalarca sürebilir. Bunun yerine bağlantı kurma ve yanıt başlığı için
// ayrı sınırlar var; takılan bir sunucu bizi sonsuza kadar bekletmesin.
var DefaultClient = &http.Client{
	Transport: &http.Transport{
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		IdleConnTimeout:       60 * time.Second,
	},
}

// download, dosyayı indirir ve SHA256'sını doğrular.
//
// Yarım kalan indirme dest + ".part" olarak durur ve bir sonraki denemede
// HTTP Range ile kaldığı yerden sürer. Doğrulama başarısız olursa parça
// dosyası silinir: bozuk bir indirmeyi tekrar tekrar sürdürmeye çalışmak,
// sonsuza kadar başarısız olan bir döngü demek.
func download(ctx context.Context, client *http.Client, url, dest, wantSHA string, wantSize int64, onProgress func(Progress)) error {
	if client == nil {
		client = DefaultClient
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}

	partPath := dest + ".part"
	digest := sha256.New()
	resumeFrom, err := seedFromPartial(partPath, digest)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if resumeFrom > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", resumeFrom))
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("runtime: indirme başarısız: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		if resumeFrom > 0 {
			// Sunucu Range'i yok saydı: baştan başlıyoruz, özeti de sıfırla.
			digest = sha256.New()
			resumeFrom = 0
		}
	case http.StatusPartialContent:
		// Sürdürme kabul edildi.
	case http.StatusRequestedRangeNotSatisfiable:
		// Parça dosyası sunucudaki dosyadan büyük ya da dosya değişmiş.
		os.Remove(partPath)
		return fmt.Errorf("runtime: yarım indirme sunucudaki dosyayla uyuşmuyor; tekrar deneyin")
	default:
		return fmt.Errorf("runtime: indirme başarısız: %s → %s", url, resp.Status)
	}

	total := wantSize
	if total == 0 && resp.ContentLength > 0 {
		total = resp.ContentLength + resumeFrom
	}

	flags := os.O_CREATE | os.O_WRONLY
	if resumeFrom > 0 {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	file, err := os.OpenFile(partPath, flags, 0o644)
	if err != nil {
		return err
	}

	written, copyErr := copyWithProgress(file, io.TeeReader(resp.Body, digest), resumeFrom, total, onProgress)
	closeErr := file.Close()
	if copyErr != nil {
		// Parça dosyasını bırakıyoruz: ağ koptuysa bir sonraki deneme
		// kaldığı yerden sürsün.
		return fmt.Errorf("runtime: indirme yarıda kesildi: %w", copyErr)
	}
	if closeErr != nil {
		return closeErr
	}

	if wantSize > 0 && written != wantSize {
		os.Remove(partPath)
		return fmt.Errorf("runtime: dosya boyutu beklenenden farklı: %d, beklenen %d", written, wantSize)
	}

	got := hex.EncodeToString(digest.Sum(nil))
	if !strings.EqualFold(got, wantSHA) {
		os.Remove(partPath)
		return fmt.Errorf("runtime: SHA256 uyuşmuyor\n  beklenen: %s\n  bulunan : %s\n  kaynak  : %s",
			strings.ToLower(wantSHA), got, url)
	}

	return os.Rename(partPath, dest)
}

// seedFromPartial, varsa yarım dosyayı okuyup özeti besler ve nereden
// devam edileceğini döner.
func seedFromPartial(partPath string, digest hash.Hash) (int64, error) {
	info, err := os.Stat(partPath)
	if err != nil || info.Size() == 0 {
		return 0, nil
	}

	f, err := os.Open(partPath)
	if err != nil {
		return 0, nil
	}
	defer f.Close()

	n, err := io.Copy(digest, f)
	if err != nil {
		// Parça okunamıyorsa baştan başlamak, kırık durumla uğraşmaktan iyi.
		os.Remove(partPath)
		return 0, nil
	}
	return n, nil
}

// copyWithProgress, veriyi kopyalarken ilerlemeyi bildirir.
func copyWithProgress(dst io.Writer, src io.Reader, alreadyHave, total int64, onProgress func(Progress)) (int64, error) {
	buf := make([]byte, 256*1024)
	written := alreadyHave
	lastReport := time.Now()

	for {
		n, err := src.Read(buf)
		if n > 0 {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return written, werr
			}
			written += int64(n)
			// Saniyede birkaç kez bildirmek yeterli; her okumada çağırmak
			// terminali kilitliyor.
			if onProgress != nil && time.Since(lastReport) > 200*time.Millisecond {
				onProgress(Progress{Downloaded: written, Total: total})
				lastReport = time.Now()
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return written, err
		}
	}
	if onProgress != nil {
		onProgress(Progress{Downloaded: written, Total: total})
	}
	return written, nil
}
