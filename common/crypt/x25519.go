package crypt

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/curve25519"
)

const (
	wgClientSeed = "tjxt-wg-client:"
	wgServerSeed = "tjxt-wg-server:"
	wgAddrSeed   = "tjxt-wg-addr:"
)

func GenX25519Private(data []byte) []byte {
	key := sha256.Sum256(data)
	key[0] &= 248
	key[31] &= 127
	key[31] |= 64
	return key[:]
}

func X25519Public(private []byte) []byte {
	var priv, pub [32]byte
	copy(priv[:], private)
	curve25519.ScalarBaseMult(&pub, &priv)
	return pub[:]
}

func KeyB64(raw []byte) string {
	return base64.StdEncoding.EncodeToString(raw)
}

func ParseKey32(raw string) ([]byte, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("empty key")
	}
	if len(raw) == 64 {
		if decoded, err := hex.DecodeString(raw); err == nil && len(decoded) == 32 {
			return decoded, nil
		}
	}
	trimmed := strings.TrimRight(raw, "=")
	var decoded []byte
	var err error
	if strings.ContainsAny(trimmed, "+/") {
		decoded, err = base64.StdEncoding.DecodeString(raw)
		if err != nil {
			decoded, err = base64.RawStdEncoding.DecodeString(trimmed)
		}
	} else {
		decoded, err = base64.RawURLEncoding.DecodeString(trimmed)
		if err != nil {
			decoded, err = base64.StdEncoding.DecodeString(raw)
		}
	}
	if err != nil {
		return nil, err
	}
	if len(decoded) != 32 {
		return nil, fmt.Errorf("key must be 32 bytes")
	}
	return decoded, nil
}

func WireGuardClientPrivate(uuid string) []byte {
	return GenX25519Private([]byte(wgClientSeed + uuid))
}

func WireGuardClientPublic(uuid string) []byte {
	return X25519Public(WireGuardClientPrivate(uuid))
}

func WireGuardServerPrivate(host string, port int, secretKey string) []byte {
	if parsed, err := ParseKey32(secretKey); err == nil {
		return parsed
	}
	return GenX25519Private([]byte(wgServerSeed + host + ":" + strconv.Itoa(port)))
}

func WireGuardServerPublic(host string, port int, secretKey string) []byte {
	return X25519Public(WireGuardServerPrivate(host, port, secretKey))
}

func WireGuardClientAddress(uuid string) string {
	sum := sha256.Sum256([]byte(wgAddrSeed + uuid))
	a := int(sum[0])
	b := int(sum[1])
	c := int(sum[2])%254 + 1
	if c == 255 {
		c = 1
	}
	return fmt.Sprintf("10.%d.%d.%d/32", a, b, c)
}
