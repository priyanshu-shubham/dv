package webpush

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"time"

	"dv/internal/secret"
)

// kept is what push.json holds: the VAPID key, sealed like a bot's token
// (package secret), and the devices subscribed with it, which are lost with
// it. It is the computer's, every dv's, in a file only the user can read.
type kept struct {
	Key     string   `json:"key,omitempty"`
	Devices []Device `json:"devices,omitempty"`
}

// Device is a browser's subscription: where its push service takes messages
// for it, and what they are sealed for it with.
type Device struct {
	Endpoint string    `json:"endpoint"`
	P256dh   string    `json:"p256dh"` // base64url, as the browser gives them
	Auth     string    `json:"auth"`
	Name     string    `json:"name,omitempty"`
	Added    time.Time `json:"added"`
	// DV is the dv it subscribed through, which alone sends to it: a page's
	// service worker opens only its own dv's sessions.
	DV string `json:"dv"`
}

// sealedAs binds the sealed key to what it is.
const sealedAs = "dv web push key"

var errNoKey = errors.New("no key to sign notifications with")

func (p *Push) path() string { return filepath.Join(p.dir, "push.json") }

// load reads what is kept, and opens its key; one that will not open - this
// computer's key lost, or the file copied from another - is none. found is
// whether there is a file. Callers hold p.mu.
func (p *Push) load() (k kept, v *vapid, found bool) {
	b, err := os.ReadFile(p.path())
	if err != nil || json.Unmarshal(b, &k) != nil {
		return kept{}, nil, err == nil
	}
	if k.Key == "" {
		return k, nil, true
	}
	if k.Key == p.sealed {
		return k, p.vapid, true
	}
	if key, err := secret.Key(p.keyDir, false); err == nil {
		if der, err := secret.Open(key, k.Key, sealedAs); err == nil {
			v, _ = openVapid([]byte(der))
		}
	}
	if v != nil {
		p.sealed, p.vapid = k.Key, v
	}
	return k, v, true
}

// key is the VAPID key, made the first time one is needed when create is set.
// A new one starts the devices afresh: they subscribed with the last.
func (p *Push) key(create bool) (*vapid, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, v, found := p.load()
	if v != nil {
		return v, nil
	}
	if !create {
		return nil, errNoKey
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	sk, err := secret.Key(p.keyDir, true)
	if err != nil {
		return nil, fmt.Errorf("could not make the key the push key is kept with: %w", err)
	}
	sealed, err := secret.Seal(sk, string(der), sealedAs)
	if err != nil {
		return nil, err
	}
	b, _ := json.MarshalIndent(kept{Key: sealed}, "", "  ")
	// Of dvs making the first at the same moment, the one written first is it.
	err = secret.WritePrivate(p.path(), b, found)
	if errors.Is(err, fs.ErrExist) {
		if _, v, _ := p.load(); v != nil {
			return v, nil
		}
		err = secret.WritePrivate(p.path(), b, true)
	}
	if err != nil {
		return nil, err
	}
	if _, v, _ = p.load(); v == nil {
		return nil, errNoKey
	}
	return v, nil
}

// change rewrites what is kept, the key as it is.
func (p *Push) change(edit func(*kept)) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	k, _, _ := p.load()
	edit(&k)
	b, _ := json.MarshalIndent(k, "", "  ")
	return secret.WritePrivate(p.path(), b, true)
}

// devices are those this dv sends to.
func (p *Push) devices() []Device {
	p.mu.Lock()
	k, _, _ := p.load()
	p.mu.Unlock()
	return slices.DeleteFunc(k.Devices, func(d Device) bool { return d.DV != p.self })
}

// device is this dv's at endpoint.
func (p *Push) device(endpoint string) (Device, bool) {
	ds := p.devices()
	i := slices.IndexFunc(ds, func(d Device) bool { return d.Endpoint == endpoint })
	if i < 0 {
		return Device{}, false
	}
	return ds[i], true
}

// add keeps d, in place of the subscription at its endpoint, whichever dv
// had it.
func (p *Push) add(d Device) error {
	return p.change(func(k *kept) {
		k.Devices = append(slices.DeleteFunc(k.Devices, func(o Device) bool { return o.Endpoint == d.Endpoint }), d)
	})
}

func (p *Push) forget(endpoint string) error {
	return p.change(func(k *kept) {
		k.Devices = slices.DeleteFunc(k.Devices, func(d Device) bool { return d.Endpoint == endpoint })
	})
}
