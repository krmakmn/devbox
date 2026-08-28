package main

import goruntime "runtime"

// goos ve goarch, manifest eşleşmesinde kullanılan platform bilgisini döner.
// Ayrı fonksiyonlar olarak duruyorlar ki çapraz platform senaryolarını
// denemek gerektiğinde tek yerden değiştirilebilsinler.
func goos() string   { return goruntime.GOOS }
func goarch() string { return goruntime.GOARCH }
