// Package acme, yerel bir ACME (RFC 8555) sunucusu sağlar.
//
// # Ne işe yarıyor
//
// DevBox'ın kendi CA'sı bir dizindeki projelere sertifika üretiyor. Ama
// geliştirme ortamındaki her şey o dizinden çıkmıyor: WSL2'deki bir
// konteynerde koşan Caddy, Traefik ya da certbot kendi sertifikasını
// kendi almak istiyor. Onların bildiği tek dil ACME. Yerel bir ACME
// sunucusu, "sertifikayı elle üret, konteynere kopyala" adımını tümden
// kaldırıyor: konteyner ACME_CA=http://... deyip sertifikasını alıyor ve
// tarayıcı zaten kökümüze güvendiği için uyarı çıkmıyor.
//
// # Neden kendi sunucumuz
//
// Pebble ve Smallstep gibi hazır sunucular var. İkisi de ayrı bir ikili,
// ayrı bir CA ve ayrı bir güven zinciri demek — oysa bizim CA'mız zaten
// kurulu ve güven depolarına eklenmiş durumda. İkinci bir CA, kullanıcının
// iki kök sertifikaya güvenmesi anlamına gelirdi. Protokolün yakalama
// tarafı (posta yakalayıcıda olduğu gibi) küçük ve sınırları belli.
//
// # Neden düz HTTP
//
// Sunucu yalnız geri döngüyü dinliyor ve ACME'nin kendisi imzalı: her
// istek istemcinin hesap anahtarıyla imzalanmış bir JWS. Araya girme
// riski geri döngüde yok. TLS eklemek, sertifikasını kimin vereceği
// sorusunu doğuruyordu (kendi kendine imza).
package acme

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math/big"
)

// jwsEnvelope, RFC 8555 §6.2'deki düzleştirilmiş JWS.
type jwsEnvelope struct {
	Protected string `json:"protected"`
	Payload   string `json:"payload"`
	Signature string `json:"signature"`
}

// protectedHeader, imzalı başlık.
type protectedHeader struct {
	Alg   string          `json:"alg"`
	Nonce string          `json:"nonce"`
	URL   string          `json:"url"`
	KID   string          `json:"kid,omitempty"`
	JWK   json.RawMessage `json:"jwk,omitempty"`
}

// jwsRequest, doğrulanmış bir istek.
type jwsRequest struct {
	Header  protectedHeader
	Payload []byte

	// Key, isteği imzalayan açık anahtar.
	Key crypto.PublicKey

	// Thumbprint, anahtarın RFC 7638 parmak izi (base64url).
	Thumbprint string
}

// parseJWS, gövdeyi çözer ve imzayı doğrular.
//
// Anahtar iki yoldan gelebiliyor: yeni hesapta gömülü JWK, sonrasında
// hesap adresini gösteren kid. İkincisinde anahtarı çağıran sağlıyor.
func parseJWS(body []byte, lookup func(kid string) (crypto.PublicKey, string, bool)) (*jwsRequest, error) {
	var env jwsEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("JWS çözülemedi: %w", err)
	}
	protectedRaw, err := base64.RawURLEncoding.DecodeString(env.Protected)
	if err != nil {
		return nil, fmt.Errorf("protected başlığı çözülemedi: %w", err)
	}
	var header protectedHeader
	if err := json.Unmarshal(protectedRaw, &header); err != nil {
		return nil, fmt.Errorf("protected başlığı ayrıştırılamadı: %w", err)
	}
	payload, err := base64.RawURLEncoding.DecodeString(env.Payload)
	if err != nil {
		return nil, fmt.Errorf("payload çözülemedi: %w", err)
	}
	signature, err := base64.RawURLEncoding.DecodeString(env.Signature)
	if err != nil {
		return nil, fmt.Errorf("imza çözülemedi: %w", err)
	}

	var (
		key        crypto.PublicKey
		thumbprint string
	)
	switch {
	case len(header.JWK) > 0 && header.KID != "":
		return nil, fmt.Errorf("jwk ve kid birlikte kullanılamaz")
	case len(header.JWK) > 0:
		key, thumbprint, err = parseJWK(header.JWK)
		if err != nil {
			return nil, err
		}
	case header.KID != "":
		var ok bool
		key, thumbprint, ok = lookup(header.KID)
		if !ok {
			return nil, fmt.Errorf("bilinmeyen hesap: %s", header.KID)
		}
	default:
		return nil, fmt.Errorf("imzalayan anahtar belirtilmemiş")
	}

	signed := []byte(env.Protected + "." + env.Payload)
	if err := verifySignature(header.Alg, key, signed, signature); err != nil {
		return nil, err
	}

	return &jwsRequest{Header: header, Payload: payload, Key: key, Thumbprint: thumbprint}, nil
}

// verifySignature, imzayı algoritmaya göre doğrular.
func verifySignature(alg string, key crypto.PublicKey, signed, signature []byte) error {
	switch alg {
	case "ES256", "ES384", "ES512":
		pub, ok := key.(*ecdsa.PublicKey)
		if !ok {
			return fmt.Errorf("%s için ECDSA anahtarı bekleniyordu", alg)
		}
		hashed, size := ecdsaHash(alg, signed)
		// ECDSA imzası JWS'te r ve s'in sabit uzunlukta birleşimi;
		// ASN.1 değil.
		if len(signature) != 2*size {
			return fmt.Errorf("ECDSA imza uzunluğu %d, %d bekleniyordu", len(signature), 2*size)
		}
		r := new(big.Int).SetBytes(signature[:size])
		s := new(big.Int).SetBytes(signature[size:])
		if !ecdsa.Verify(pub, hashed, r, s) {
			return fmt.Errorf("imza doğrulanamadı")
		}
		return nil

	case "RS256", "RS384", "RS512":
		pub, ok := key.(*rsa.PublicKey)
		if !ok {
			return fmt.Errorf("%s için RSA anahtarı bekleniyordu", alg)
		}
		hashed, hash := rsaHash(alg, signed)
		if err := rsa.VerifyPKCS1v15(pub, hash, hashed, signature); err != nil {
			return fmt.Errorf("imza doğrulanamadı: %w", err)
		}
		return nil

	default:
		return fmt.Errorf("desteklenmeyen imza algoritması %q", alg)
	}
}

func ecdsaHash(alg string, data []byte) ([]byte, int) {
	switch alg {
	case "ES384":
		sum := sha512.Sum384(data)
		return sum[:], 48
	case "ES512":
		sum := sha512.Sum512(data)
		return sum[:], 66
	default:
		sum := sha256.Sum256(data)
		return sum[:], 32
	}
}

func rsaHash(alg string, data []byte) ([]byte, crypto.Hash) {
	switch alg {
	case "RS384":
		sum := sha512.Sum384(data)
		return sum[:], crypto.SHA384
	case "RS512":
		sum := sha512.Sum512(data)
		return sum[:], crypto.SHA512
	default:
		sum := sha256.Sum256(data)
		return sum[:], crypto.SHA256
	}
}

// jwkFields, JWK'nın tanıdığımız alanları.
type jwkFields struct {
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
	N   string `json:"n"`
	E   string `json:"e"`
}

// parseJWK, JWK'yı açık anahtara ve parmak izine çevirir.
func parseJWK(raw json.RawMessage) (crypto.PublicKey, string, error) {
	var jwk jwkFields
	if err := json.Unmarshal(raw, &jwk); err != nil {
		return nil, "", fmt.Errorf("JWK ayrıştırılamadı: %w", err)
	}

	switch jwk.Kty {
	case "EC":
		curve, size, err := curveFor(jwk.Crv)
		if err != nil {
			return nil, "", err
		}
		x, err := decodeCoordinate(jwk.X, size)
		if err != nil {
			return nil, "", err
		}
		y, err := decodeCoordinate(jwk.Y, size)
		if err != nil {
			return nil, "", err
		}
		pub := &ecdsa.PublicKey{Curve: curve, X: x, Y: y}
		if !curve.IsOnCurve(x, y) {
			return nil, "", fmt.Errorf("JWK noktası eğri üzerinde değil")
		}
		// RFC 7638: parmak izi, yalnız zorunlu alanları içeren ve
		// anahtarları sıralı olan en sade JSON'ın SHA-256'sı.
		return pub, thumbprint(fmt.Sprintf(`{"crv":%q,"kty":"EC","x":%q,"y":%q}`,
			jwk.Crv, jwk.X, jwk.Y)), nil

	case "RSA":
		nBytes, err := base64.RawURLEncoding.DecodeString(jwk.N)
		if err != nil {
			return nil, "", fmt.Errorf("RSA modülü çözülemedi: %w", err)
		}
		eBytes, err := base64.RawURLEncoding.DecodeString(jwk.E)
		if err != nil {
			return nil, "", fmt.Errorf("RSA üssü çözülemedi: %w", err)
		}
		if len(eBytes) == 0 || len(eBytes) > 8 {
			return nil, "", fmt.Errorf("RSA üssü geçersiz")
		}
		var padded [8]byte
		copy(padded[8-len(eBytes):], eBytes)
		pub := &rsa.PublicKey{
			N: new(big.Int).SetBytes(nBytes),
			E: int(binary.BigEndian.Uint64(padded[:])),
		}
		if pub.N.Sign() == 0 || pub.E < 2 {
			return nil, "", fmt.Errorf("RSA anahtarı geçersiz")
		}
		return pub, thumbprint(fmt.Sprintf(`{"e":%q,"kty":"RSA","n":%q}`, jwk.E, jwk.N)), nil

	default:
		return nil, "", fmt.Errorf("desteklenmeyen anahtar türü %q", jwk.Kty)
	}
}

func curveFor(crv string) (elliptic.Curve, int, error) {
	switch crv {
	case "P-256":
		return elliptic.P256(), 32, nil
	case "P-384":
		return elliptic.P384(), 48, nil
	case "P-521":
		return elliptic.P521(), 66, nil
	default:
		return nil, 0, fmt.Errorf("desteklenmeyen eğri %q", crv)
	}
}

// decodeCoordinate, JWK koordinatını çözer.
//
// Uzunluk denetimi önemli: JWK'da koordinatlar eğrinin boyutunda sabit
// uzunlukta olmak zorunda. Kısa bir değeri kabul etmek, aynı anahtarın
// farklı parmak izleriyle görünmesine yol açardı.
func decodeCoordinate(value string, size int) (*big.Int, error) {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return nil, fmt.Errorf("koordinat çözülemedi: %w", err)
	}
	if len(raw) != size {
		return nil, fmt.Errorf("koordinat uzunluğu %d, %d bekleniyordu", len(raw), size)
	}
	return new(big.Int).SetBytes(raw), nil
}

func thumbprint(canonical string) string {
	sum := sha256.Sum256([]byte(canonical))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// keyAuthorization, http-01 için beklenen gövde.
func keyAuthorization(token, thumbprint string) string {
	return token + "." + thumbprint
}
