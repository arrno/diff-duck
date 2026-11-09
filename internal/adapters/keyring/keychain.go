// Package keyring implements ports.SecretStore and ports.KeySource with the OS keychain (macOS Keychain, Windows
// Credential Manager, or the Secret Service on Linux) through go-keyring, which needs no cgo.
//
// K1: a key is passed to the keychain and returned from it, and nowhere else. Errors from
// operations that carry a key never include the library's error text, since that text is not
// promised to be free of the secret.
package keyring

import (
	"errors"
	"fmt"

	"github.com/arrno/diff-duck/internal/domain"
	"github.com/arrno/diff-duck/internal/services/ports"
	gokeyring "github.com/zalando/go-keyring"
)

// Service is the keychain service name every DiffDuck key is filed under.
const Service = "diff-duck"

var (
	errSave  = errors.New("could not save the API key to the OS keychain")
	errRead  = errors.New("could not read the OS keychain")
	errClear = errors.New("could not clear the API key from the OS keychain")
)

// Keychain stores one API key per model provider, using the provider ID as the account name.
type Keychain struct {
	service string
}

// New returns a keychain filed under Service.
func New() *Keychain { return &Keychain{service: Service} }

func account(provider domain.ModelProvider) (string, error) {
	if !provider.Valid() {
		return "", domain.ErrUnknownProvider
	}
	if !provider.NeedsKey() {
		return "", fmt.Errorf("%s does not use an API key", provider.Label())
	}
	return string(provider), nil
}

// SaveKey stores the provider's key, replacing any saved one.
func (k *Keychain) SaveKey(provider domain.ModelProvider, key string) error {
	user, err := account(provider)
	if err != nil {
		return err
	}
	if key == "" {
		return errors.New("an empty API key cannot be saved")
	}
	if err := gokeyring.Set(k.service, user, key); err != nil {
		if errors.Is(err, gokeyring.ErrSetDataTooBig) {
			return fmt.Errorf("%w: the key is too long", errSave)
		}
		return errSave
	}
	return nil
}

// LoadKey returns the provider's key, or ports.ErrKeyNotSaved.
func (k *Keychain) LoadKey(provider domain.ModelProvider) (string, error) {
	user, err := account(provider)
	if err != nil {
		return "", err
	}
	key, err := gokeyring.Get(k.service, user)
	if errors.Is(err, gokeyring.ErrNotFound) {
		return "", ports.ErrKeyNotSaved
	}
	if err != nil {
		return "", errRead
	}
	return key, nil
}

// HasKey reports whether a key is saved for the provider. go-keyring has no lookup that skips
// the secret, so the key is read and dropped at once; it is never returned or kept.
func (k *Keychain) HasKey(provider domain.ModelProvider) (bool, error) {
	_, err := k.LoadKey(provider)
	if errors.Is(err, ports.ErrKeyNotSaved) {
		return false, nil
	}
	return err == nil, err
}

// ClearKey removes the provider's key. Clearing a key that is not saved is not an error.
func (k *Keychain) ClearKey(provider domain.ModelProvider) error {
	user, err := account(provider)
	if err != nil {
		return err
	}
	if err := gokeyring.Delete(k.service, user); err != nil && !errors.Is(err, gokeyring.ErrNotFound) {
		return errClear
	}
	return nil
}
