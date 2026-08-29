//go:build ignore

// Duman testinde kullanılan en küçük arka uç.
//
// Neden indirilen bir sunucu değil: testin amacı DevBox'ı sınamak, ağ
// erişimini değil. Kendi ürettiğimiz bir ikili, koşucunun internet
// durumundan bağımsız çalışıyor.
package main

import (
	"fmt"
	"net/http"
	"os"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "3999"
	}
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Arka-Uc", "duman")
		fmt.Fprintf(w, "DEVBOX-DUMAN-TESTI host=%s yol=%s", r.Host, r.URL.Path)
	})
	fmt.Println("arka uç dinliyor:", port)
	http.ListenAndServe("127.0.0.1:"+port, nil)
}
