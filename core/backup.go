package core

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strings"

	"golang.org/x/crypto/scrypt"
)

// The app's backup (subscriptions, settings and servers with their logins), sealed with a
// passphrase: scrypt makes the key, AES-256-GCM seals. The file is text, so it can be shared as a
// file or pasted.

const backupPrefix = "xtune-backup:v1:"

func backupKey(pass string, salt []byte) ([]byte, error) {
	return scrypt.Key([]byte(pass), salt, 1<<15, 8, 1, 32)
}

// SealBackup seals data (the app's JSON) with pass.
func SealBackup(data, pass string) (string, error) {
	if len(pass) < 8 {
		return "", errors.New("use a passphrase of at least 8 characters")
	}
	salt := make([]byte, 16)
	rand.Read(salt)
	key, err := backupKey(pass, salt)
	if err != nil {
		return "", err
	}
	block, _ := aes.NewCipher(key)
	gcm, _ := cipher.NewGCM(block)
	nonce := make([]byte, gcm.NonceSize())
	rand.Read(nonce)
	sealed := gcm.Seal(nil, nonce, []byte(data), []byte(backupPrefix))
	out := append(append(salt, nonce...), sealed...)
	return backupPrefix + base64.StdEncoding.EncodeToString(out), nil
}

// OpenBackup opens what SealBackup made.
func OpenBackup(text, pass string) (string, error) {
	b64, ok := strings.CutPrefix(strings.TrimSpace(text), backupPrefix)
	if !ok {
		return "", errors.New("this is not an xtune backup")
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(raw) < 16+12+16 {
		return "", errors.New("the backup is damaged")
	}
	key, err := backupKey(pass, raw[:16])
	if err != nil {
		return "", err
	}
	block, _ := aes.NewCipher(key)
	gcm, _ := cipher.NewGCM(block)
	data, err := gcm.Open(nil, raw[16:16+gcm.NonceSize()], raw[16+gcm.NonceSize():], []byte(backupPrefix))
	if err != nil {
		return "", errors.New("wrong passphrase, or the backup is damaged")
	}
	return string(data), nil
}
