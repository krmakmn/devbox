package procstat

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// clockTicks, /proc/<pid>/stat'taki işlemci sürelerinin birimi.
//
// POSIX bunu sysconf(_SC_CLK_TCK) ile veriyor; Linux'ta çekirdek
// derlemesinden bağımsız olarak kullanıcı alanına 100 olarak görünüyor
// (USER_HZ). cgo'suz okumanın taşınabilir bir yolu yok ve değer 100
// dışında bir şey olan bir dağıtım pratikte yok.
const clockTicks = 100.0

func read(pid int) (Stat, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return Stat{}, err
	}

	// Alan 2 (comm) parantez içinde ve boşluk içerebiliyor; ayrıştırmaya
	// kapanış parantezinden sonra başlamak gerekiyor. "(php cgi)" gibi
	// bir ad, boşluğa göre bölen bir ayrıştırıcıyı kaydırır.
	line := string(data)
	close := strings.LastIndex(line, ")")
	if close < 0 || close+2 > len(line) {
		return Stat{}, fmt.Errorf("procstat: /proc/%d/stat çözümlenemedi", pid)
	}
	fields := strings.Fields(line[close+2:])
	// Kapanıştan sonraki ilk alan 3. alan (state); utime 14, stime 15.
	const utimeIdx, stimeIdx, rssIdx = 11, 12, 21
	if len(fields) <= rssIdx {
		return Stat{}, fmt.Errorf("procstat: /proc/%d/stat beklenenden kısa", pid)
	}

	utime, err := strconv.ParseUint(fields[utimeIdx], 10, 64)
	if err != nil {
		return Stat{}, err
	}
	stime, err := strconv.ParseUint(fields[stimeIdx], 10, 64)
	if err != nil {
		return Stat{}, err
	}
	// rss sayfa cinsinden.
	rssPages, err := strconv.ParseUint(fields[rssIdx], 10, 64)
	if err != nil {
		return Stat{}, err
	}

	return Stat{
		RSS:        rssPages * uint64(os.Getpagesize()),
		CPUSeconds: float64(utime+stime) / clockTicks,
	}, nil
}
