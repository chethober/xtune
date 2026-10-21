package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"

	"golang.org/x/crypto/scrypt"
)

// Encrypted backups: the backup JSON sealed with AES-256-GCM under a key derived from a
// passphrase with scrypt. The bot sends these, since a backup holds every secret of the server.

const sealedFormat = "xtune-encrypted-backup-1"

type sealed struct {
	Format string `json:"format"`
	Salt   []byte `json:"salt"`
	Nonce  []byte `json:"nonce"`
	Data   []byte `json:"data"`
}

func sealKey(pass string, salt []byte) ([]byte, error) {
	return scrypt.Key([]byte(pass), salt, 1<<15, 8, 1, 32)
}

func seal(plain []byte, pass string) ([]byte, error) {
	if pass == "" {
		return nil, errors.New("empty passphrase")
	}
	s := sealed{Format: sealedFormat, Salt: make([]byte, 16), Nonce: make([]byte, 12)}
	rand.Read(s.Salt)
	rand.Read(s.Nonce)
	key, err := sealKey(pass, s.Salt)
	if err != nil {
		return nil, err
	}
	blk, _ := aes.NewCipher(key)
	gcm, _ := cipher.NewGCM(blk)
	s.Data = gcm.Seal(nil, s.Nonce, plain, []byte(sealedFormat))
	return json.MarshalIndent(s, "", " ")
}

// unseal returns raw unchanged when it is not sealed, and the plain text when it is.
func unseal(raw []byte, passphrase func() (string, error)) ([]byte, error) {
	var s sealed
	if json.Unmarshal(raw, &s) != nil || s.Format != sealedFormat {
		return raw, nil
	}
	pass, err := passphrase()
	if err != nil {
		return nil, err
	}
	key, err := sealKey(pass, s.Salt)
	if err != nil {
		return nil, err
	}
	blk, _ := aes.NewCipher(key)
	gcm, _ := cipher.NewGCM(blk)
	if len(s.Nonce) != gcm.NonceSize() {
		return nil, errors.New("damaged encrypted backup")
	}
	plain, err := gcm.Open(nil, s.Nonce, s.Data, []byte(sealedFormat))
	if err != nil {
		return nil, errors.New("wrong passphrase, or a damaged backup")
	}
	return plain, nil
}

// backupPassphrase reads the passphrase of an encrypted backup: XTUNE_BACKUP_PASSWORD, or asked.
func backupPassphrase() (string, error) {
	if p := os.Getenv("XTUNE_BACKUP_PASSWORD"); p != "" {
		return p, nil
	}
	return cliPrompt("Backup passphrase", true)
}

// newPassphrase asks for a new passphrase twice, unless XTUNE_BACKUP_PASSWORD is set.
func newPassphrase() (string, error) {
	if p := os.Getenv("XTUNE_BACKUP_PASSWORD"); p != "" {
		return p, nil
	}
	p, err := cliPrompt("Backup passphrase (needed to restore; at least 8 characters)", true)
	if err != nil {
		return "", err
	}
	if len(p) < 8 {
		return "", errors.New("the passphrase is too short")
	}
	again, err := cliPrompt("Again", true)
	if err != nil {
		return "", err
	}
	if again != p {
		return "", errors.New("the passphrases differ")
	}
	return p, nil
}
