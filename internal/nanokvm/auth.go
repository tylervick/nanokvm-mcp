// Package nanokvm is a client for the NanoKVM firmware HTTP API.
//
// auth.go reimplements the CryptoJS-compatible password encryption used by the
// NanoKVM web frontend. The scheme (OpenSSL "Salted__" format, EVP_BytesToKey
// MD5 key derivation, fixed passphrase) is dictated by the upstream
// GPL-3.0 project github.com/sipeed/NanoKVM; this file is a clean-room Go
// implementation of that wire format and is licensed GPL-3.0 accordingly.
package nanokvm

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/rand"
	"encoding/base64"
	"net/url"
)

const nanokvmPassphrase = "nanokvm-sipeed-2024"

// evpBytesToKey derives key+iv the way OpenSSL EVP_BytesToKey (MD5) does,
// matching CryptoJS's default passphrase handling.
func evpBytesToKey(pass, salt []byte, keyLen, ivLen int) (key, iv []byte) {
	var d, prev []byte
	for len(d) < keyLen+ivLen {
		h := md5.New()
		h.Write(prev)
		h.Write(pass)
		h.Write(salt)
		prev = h.Sum(nil)
		d = append(d, prev...)
	}
	return d[:keyLen], d[keyLen : keyLen+ivLen]
}

func pkcs7Pad(b []byte, blockSize int) []byte {
	n := blockSize - len(b)%blockSize
	return append(b, bytes.Repeat([]byte{byte(n)}, n)...)
}

// EncryptPassword returns the URL-encoded base64 of the OpenSSL "Salted__"
// container that the NanoKVM login endpoint expects.
func EncryptPassword(password string) (string, error) {
	salt := make([]byte, 8)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, iv := evpBytesToKey([]byte(nanokvmPassphrase), salt, 32, 16)
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	plain := pkcs7Pad([]byte(password), aes.BlockSize)
	ct := make([]byte, len(plain))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(ct, plain)

	container := append(append([]byte("Salted__"), salt...), ct...)
	b64 := base64.StdEncoding.EncodeToString(container)
	return url.QueryEscape(b64), nil
}
