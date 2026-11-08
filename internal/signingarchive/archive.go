// Package signingarchive authenticates bounded private signer snapshots.
package signingarchive

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"path/filepath"
)

const header = "IS_SIGNER_ARCHIVE\x00v1\x00"
const maxPlaintext = 512 << 20

var errPrivate = errors.New("private owned regular files and private output directory required")

func readPrivate(path string, limit int64) ([]byte, error) {
	if !filepath.IsAbs(path) {
		return nil, errPrivate
	}
	f, err := openPrivate(path, false)
	if err != nil {
		return nil, errPrivate
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() <= 0 || info.Size() > limit {
		return nil, errPrivate
	}
	body, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(body)) > limit || int64(len(body)) != info.Size() {
		return nil, errPrivate
	}
	return body, nil
}

func publish(path string, body []byte) (result error) {
	if !filepath.IsAbs(path) {
		return errPrivate
	}
	parent, err := openPrivate(filepath.Dir(path), true)
	if err != nil {
		return errPrivate
	}
	defer parent.Close()
	temp, err := os.CreateTemp(filepath.Dir(path), ".signer-archive-")
	if err != nil {
		return errPrivate
	}
	defer os.Remove(temp.Name())
	defer temp.Close()
	if _, err = temp.Write(body); err != nil {
		return err
	}
	if err = temp.Sync(); err != nil {
		return err
	}
	if err = temp.Close(); err != nil {
		return err
	}
	// Link publishes exclusively, including when an occupied path is a symlink.
	if err = os.Link(temp.Name(), path); err != nil {
		return err
	}
	if err = parent.Sync(); err != nil {
		_ = os.Remove(path)
		return err
	}
	return nil
}

// GenerateKey creates a random 256-bit key without replacing an existing file.
func GenerateKey(output string) error {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return err
	}
	return publish(output, key)
}

func aead(keyFile string) (cipher.AEAD, error) {
	key, err := readPrivate(keyFile, 32)
	if err != nil || len(key) != 32 {
		return nil, errPrivate
	}
	defer clear(key)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// Seal encrypts and authenticates a private snapshot; its version is AAD.
func Seal(keyFile, input, output string) error {
	c, err := aead(keyFile)
	if err != nil {
		return err
	}
	plain, err := readPrivate(input, maxPlaintext)
	if err != nil {
		return err
	}
	defer clear(plain)
	nonce := make([]byte, c.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return err
	}
	body := append([]byte(header), nonce...)
	body = c.Seal(body, nonce, plain, []byte(header))
	return publish(output, body)
}

// Open authenticates the entire archive before publishing any plaintext.
func Open(keyFile, input, output string) error {
	c, err := aead(keyFile)
	if err != nil {
		return err
	}
	body, err := readPrivate(input, maxPlaintext+int64(len(header)+c.NonceSize()+c.Overhead()))
	if err != nil {
		return err
	}
	minimum := len(header) + c.NonceSize() + c.Overhead()
	if len(body) <= minimum || !bytes.Equal(body[:len(header)], []byte(header)) {
		return errors.New("unsupported or incomplete authenticated archive")
	}
	nonce := body[len(header) : len(header)+c.NonceSize()]
	plain, err := c.Open(nil, nonce, body[len(header)+c.NonceSize():], []byte(header))
	if err != nil {
		return errors.New("archive authentication failed")
	}
	defer clear(plain)
	return publish(output, plain)
}
