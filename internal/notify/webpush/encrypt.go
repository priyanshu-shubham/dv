package webpush

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
)

// recordSize is what the body's header says a record holds at most; a
// message is one record, well inside it.
const recordSize = 4096

// encrypt seals a message for a device as RFC 8291 has it, in RFC 8188's
// aes128gcm: its key agreed between one made for this message alone and the
// device's public key, and mixed with the device's auth secret, so only the
// device reads it. The push service carrying it sees bytes.
func encrypt(plaintext, uaPublic, authSecret []byte) ([]byte, error) {
	eph, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	salt := make([]byte, 16)
	rand.Read(salt)
	return seal(plaintext, uaPublic, authSecret, eph, salt)
}

// seal is encrypt with the message's key and salt given, as the RFC's
// example gives them.
func seal(plaintext, uaPublic, authSecret []byte, eph *ecdh.PrivateKey, salt []byte) ([]byte, error) {
	ua, err := ecdh.P256().NewPublicKey(uaPublic)
	if err != nil {
		return nil, errors.New("that is not a device's public key")
	}
	shared, err := eph.ECDH(ua)
	if err != nil {
		return nil, err
	}
	asPublic := eph.PublicKey().Bytes()
	ikm, err := hkdf.Key(sha256.New, shared, authSecret, "WebPush: info\x00"+string(uaPublic)+string(asPublic), 32)
	if err != nil {
		return nil, err
	}
	prk, err := hkdf.Extract(sha256.New, ikm, salt)
	if err != nil {
		return nil, err
	}
	cek, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		return nil, err
	}
	nonce, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	// The last record's plaintext ends in 2, before any padding; there is none.
	padded := append(plaintext[:len(plaintext):len(plaintext)], 2)
	if len(padded)+gcm.Overhead() > recordSize {
		return nil, errors.New("too long for one record")
	}
	// The header: salt, record size, and the message's public key as keyid.
	header := append([]byte{}, salt...)
	header = binary.BigEndian.AppendUint32(header, recordSize)
	header = append(header, byte(len(asPublic)))
	header = append(header, asPublic...)
	return gcm.Seal(header, nonce, padded, nil), nil
}
